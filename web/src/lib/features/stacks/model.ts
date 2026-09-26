// Stack view model (#22 track B2, #7): pure functions that turn API DTOs
// into what the stack pages show. No Svelte, no fetch: tested in
// model.spec.ts.
import type { MyPermissions, Schema } from '$lib/api/client';
import { SERVICE_ICON_CATEGORY, type TileColor } from '$lib/design/hue';
import type {
	ContainerMetrics,
	Stack,
	StackContainer,
	StackRevision,
	StackServiceStatus
} from './queries';

/** The name users see: the display name, else the Compose project name. */
export function stackTitle(s: Pick<Stack, 'name' | 'displayName'>): string {
	return s.displayName?.trim() || s.name;
}

/** The default stack icon (Lucide "layers"). */
export const STACK_ICON = 'layers';

/**
 * The stack's icon (Lucide name) and tile colour. Stacks use the blue
 * "stack" category tile (#22 brief), never a service hue. An icon override
 * in the display metadata keeps its category colour, except the default
 * stack icon itself ("layers" is also the cache icon, but on a stack it
 * means "stack").
 */
export function stackIcon(s: Pick<Stack, 'icon'>): { icon: string; color: TileColor } {
	const override = s.icon && s.icon in SERVICE_ICON_CATEGORY ? s.icon : null;
	if (!override || override === STACK_ICON) return { icon: STACK_ICON, color: 'blue' };
	return { icon: override, color: SERVICE_ICON_CATEGORY[override] };
}

/**
 * The status shown in badges: the live Engine state when Docker Manager last
 * deployed the stack (running, partial, stopped, not running), otherwise
 * what Docker Manager last did (failed deploy, down, not deployed).
 */
export function stackStatus(s: Pick<Stack, 'status' | 'engine'>): string {
	switch (s.status) {
		case 'failed':
			return 'failed';
		case 'undeployed':
			return 'undeployed';
		case 'down':
			return 'down';
	}
	const e = s.engine?.state;
	if (e && e !== 'unknown') return e;
	return s.status;
}

export interface ServiceCounts {
	/** Services with at least one running container. */
	servicesRunning: number;
	/** Services of the definition. */
	services: number;
	containersRunning: number;
	containers: number;
}

/** Service and container counts from the last observed Engine state. */
export function serviceCounts(s: Pick<Stack, 'services' | 'engine'>): ServiceCounts {
	const states = s.engine?.services ?? [];
	const names = new Set([
		...(s.services ?? []).map((x) => x.name),
		...states.map((x) => x.service)
	]);
	return {
		servicesRunning: states.filter((x) => x.running > 0).length,
		services: names.size,
		containersRunning: states.reduce((n, x) => n + x.running, 0),
		containers: states.reduce((n, x) => n + x.containers, 0)
	};
}

/** The KPI's secondary line under the stack status. */
export function statusSummary(s: Pick<Stack, 'status' | 'engine' | 'services'>): string {
	const st = stackStatus(s);
	const c = serviceCounts(s);
	switch (st) {
		case 'running':
			return c.services > 0 && c.servicesRunning === c.services
				? 'All services running'
				: `${c.servicesRunning} of ${c.services} services running`;
		case 'partial':
			return `${c.services - c.servicesRunning} of ${c.services} services not running`;
		case 'failed':
			return 'The last deploy failed';
		case 'undeployed':
			return 'Never deployed by Docker Manager';
		case 'down':
			return 'Containers and networks removed';
		case 'stopped':
			return 'Every service is stopped';
		case 'missing':
			return 'No containers on the Engine';
	}
	return '';
}

export interface PortLink {
	/** host:container, e.g. 8080:80 (with /udp for UDP). */
	label: string;
	/** A link when the environment has a service address and the port is TCP. */
	href?: string;
}

/** Published ports of a service's containers, deduplicated. */
export function servicePorts(containers: StackContainer[], serviceAddress?: string): PortLink[] {
	const seen = new Set<string>();
	const out: PortLink[] = [];
	for (const c of containers) {
		for (const p of c.ports ?? []) {
			if (!p.publicPort) continue;
			const udp = p.protocol && p.protocol !== 'tcp';
			const label = `${p.publicPort}:${p.privatePort}${udp ? `/${p.protocol}` : ''}`;
			if (seen.has(label)) continue;
			seen.add(label);
			out.push({
				label,
				href: serviceAddress && !udp ? serviceUrl(serviceAddress, p.publicPort) : undefined
			});
		}
	}
	return out;
}

/** http://address:port, with IPv6 addresses bracketed. */
export function serviceUrl(address: string, port: number): string {
	const host = address.includes(':') && !address.startsWith('[') ? `[${address}]` : address;
	return `http://${host}:${port}`;
}

/** The "open" row action's target: the first TCP port link, if any. */
export function openTarget(
	containers: StackContainer[],
	serviceAddress?: string
): string | undefined {
	return servicePorts(containers, serviceAddress).find((p) => p.href)?.href;
}

/** Running containers / containers of a service. */
export function runningOf(svc: StackServiceStatus): { running: number; total: number } {
	return {
		running: svc.containers.filter((c) => c.state === 'running').length,
		total: svc.containers.length
	};
}

/** The oldest start time of the running containers (stack uptime). */
export function upSince(services: StackServiceStatus[]): string | undefined {
	let min: number | undefined;
	let iso: string | undefined;
	for (const s of services)
		for (const c of s.containers) {
			if (c.state !== 'running' || !c.startedAt) continue;
			const t = Date.parse(c.startedAt);
			if (Number.isFinite(t) && (min === undefined || t < min)) {
				min = t;
				iso = c.startedAt;
			}
		}
	return iso;
}

function series(m: ContainerMetrics, key: string): (number | null)[] | undefined {
	return m.series.find((s) => s.key === key)?.values ?? undefined;
}

function lastValue(values: (number | null)[] | undefined): number | null {
	if (!values) return null;
	for (let i = values.length - 1; i >= 0; i--) if (values[i] !== null) return values[i];
	return null;
}

export interface StackUsage {
	/** Summed CPU per timestamp (null where no container had a sample). */
	cpu: (number | null)[];
	cpuNow: number | null;
	memoryNow: number | null;
	/** Latest CPU and memory per container name. */
	containers: Record<string, { cpu: number | null; memory: number | null }>;
}

/**
 * Sums the containers' CPU series per timestamp (a stack uses the sum of
 * its containers' share of the environment's cores, #5) and takes each
 * container's latest CPU and memory.
 */
export function stackUsage(metrics: ContainerMetrics[]): StackUsage {
	const byTime = new Map<string, number | null>();
	const containers: StackUsage['containers'] = {};
	for (const m of metrics) {
		const cpu = series(m, 'cpu.percent') ?? [];
		m.timestamps.forEach((t, i) => {
			const v = cpu[i] ?? null;
			const prev = byTime.get(t);
			byTime.set(t, v === null ? (prev ?? null) : (prev ?? 0) + v);
		});
		containers[m.container] = {
			cpu: lastValue(cpu),
			memory: lastValue(series(m, 'memory.used_bytes'))
		};
	}
	const times = [...byTime.keys()].sort();
	const cpu = times.map((t) => byTime.get(t) ?? null);
	const mem = Object.values(containers).map((c) => c.memory);
	return {
		cpu,
		cpuNow: metrics.length ? lastValue(cpu) : null,
		memoryNow: mem.some((v) => v !== null)
			? mem.reduce<number>((n, v) => n + (v ?? 0), 0)
			: null,
		containers
	};
}

/** A service's latest CPU and memory (sum over its containers). */
export function serviceUsage(
	svc: StackServiceStatus,
	usage: StackUsage
): { cpu: number | null; memory: number | null } {
	let cpu: number | null = null;
	let memory: number | null = null;
	for (const c of svc.containers) {
		const u = c.name ? usage.containers[c.name] : undefined;
		if (!u) continue;
		if (u.cpu !== null) cpu = (cpu ?? 0) + u.cpu;
		if (u.memory !== null) memory = (memory ?? 0) + u.memory;
	}
	return { cpu, memory };
}

/** A digest for display: repo@sha256:abc… → abc… (12 characters). */
export function shortDigest(d: string | undefined | null): string {
	if (!d) return '—';
	const at = d.lastIndexOf('@');
	return d
		.slice(at + 1)
		.replace(/^sha256:/, '')
		.slice(0, 12);
}

const CANDIDATE_STATUS: Record<string, string> = {
	update_available: 'Update available',
	up_to_date: 'Up to date',
	quarantined: 'Quarantined',
	check_failed: 'Check failed',
	run_failed: 'Update failed',
	ineligible: 'Not eligible',
	unchecked: 'Not checked yet'
};

/** Plain-language update candidate status (#20). */
export function candidateStatus(status: string): string {
	return CANDIDATE_STATUS[status] ?? status.replaceAll('_', ' ');
}

/** First 7 characters of a revision hash (the mockup's a1b2c3d). */
export function shortHash(hash: string): string {
	return hash.replace(/^sha256:/, '').slice(0, 7);
}

export const REVISION_SOURCES: Record<string, string> = {
	deploy: 'Deploy',
	editor: 'Stack editor',
	file_manager: 'File manager',
	external: 'Edited on disk',
	restore: 'Restore'
};

/** Plain-language source of a revision. */
export function revisionSource(source: string): string {
	return REVISION_SOURCES[source] ?? source;
}

/** Revision N · hash. */
export function revisionLabel(r: Pick<StackRevision, 'seq' | 'hash'>): string {
	return `Revision ${r.seq} (${shortHash(r.hash)})`;
}

/** Decodes a revision file for display (base64 files are binary). */
export function fileText(f: { content?: string; encoding?: string }): string | null {
	if (f.content === undefined) return null;
	if (f.encoding === 'base64') return null;
	return f.content;
}

export interface FileChange {
	path: string;
	status: 'same' | 'changed' | 'added' | 'removed' | 'binary';
	before: string;
	after: string;
}

/**
 * The files of two revisions side by side (diffs are computed in the
 * browser from both revisions' contents, #7): every path of either, in
 * path order, with what happened to it.
 */
export function compareRevisions(
	from: Pick<StackRevision, 'files'>,
	to: Pick<StackRevision, 'files'>
): FileChange[] {
	const a = new Map(from.files.map((f) => [f.path, f]));
	const b = new Map(to.files.map((f) => [f.path, f]));
	const paths = [...new Set([...a.keys(), ...b.keys()])].sort();
	return paths.map((path) => {
		const x = a.get(path);
		const y = b.get(path);
		const before = x ? fileText(x) : '';
		const after = y ? fileText(y) : '';
		if (x && y && x.sha256 === y.sha256)
			return { path, status: 'same', before: before ?? '', after: after ?? '' };
		if (before === null || after === null)
			return { path, status: 'binary', before: '', after: '' };
		return { path, status: !x ? 'added' : !y ? 'removed' : 'changed', before, after };
	});
}

/** Compose project names: lower-case letters, digits, '-' and '_' (#7). */
export function nameError(name: string): string | undefined {
	const n = name.trim();
	if (!n) return 'Enter a name.';
	if (n.length > 63) return 'Use at most 63 characters.';
	if (!/^[a-z0-9][a-z0-9_-]*$/.test(n))
		return 'Use lower-case letters, digits, dashes and underscores, starting with a letter or digit.';
	return undefined;
}

/**
 * Whether the caller holds a capability in an environment (creation
 * routes are authorized on the environment, #17): the owner always; else
 * the environment-scoped entry decides, then the instance-wide one. Only
 * for showing controls; the server still decides.
 */
export function canInEnvironment(
	perms: MyPermissions | undefined,
	capability: string,
	environmentId: string
): boolean {
	if (!perms) return false;
	if (perms.owner) return true;
	const entries = perms.entries.filter((e) => e.capability === capability);
	const env = entries.find(
		(e) => e.scope.kind === 'environment' && e.scope.environmentId === environmentId
	);
	if (env) return env.allowed;
	const inst = entries.find((e) => e.scope.kind === 'instance');
	return inst?.allowed ?? false;
}

/** Whether the caller holds a capability anywhere (any scope). */
export function canAnywhere(perms: MyPermissions | undefined, capability: string): boolean {
	if (!perms) return false;
	return perms.owner || perms.entries.some((e) => e.capability === capability && e.allowed);
}

export type MigrationFinding = Schema<'MigrationFinding'>;

/** Plain-language names of the preflight's finding codes (#35). */
const FINDING_TITLES: Record<string, string> = {
	agent_unsupported: 'Agent too old',
	anonymous_volume_skipped: 'Anonymous volume skipped',
	container_name_conflict: 'Container name taken',
	containers_running: 'Containers still running',
	device_mapping: 'Device mapping',
	directory_conflict: 'Project directory exists',
	docker_manager_resource: "Docker Manager's own resource",
	environment_offline: 'Environment offline',
	external_bind_path: 'Bind path outside the project',
	external_network_missing: 'External network missing',
	external_volume_missing: 'External volume missing',
	host_ports_unverified: 'Host ports not verified',
	image_not_pullable: 'Image cannot be pulled',
	image_rebuild: 'Image rebuilt on the destination',
	image_transfer: 'Image copied through the manager',
	image_unverified: 'Image not verified',
	insufficient_space: 'Not enough free space',
	leftovers_removed: 'Earlier partial copy removed',
	network_name_conflict: 'Network name taken',
	plain_http_transport: 'Unencrypted transfer',
	platform_mismatch: 'Platform mismatch',
	port_conflict: 'Port already in use',
	project_name_conflict: 'Compose project exists',
	project_warning: 'Definition warning',
	registry_selection: 'Registry connection',
	same_environment: 'Same environment',
	size_estimated: 'Size estimated',
	stack_name_conflict: 'Stack name taken',
	storage_unavailable: 'Storage unavailable',
	volume_definition_only: 'Volume data not migrated',
	volume_missing: 'Volume missing',
	volume_name_conflict: 'Volume name taken'
};

export function findingTitle(code: string): string {
	return (
		FINDING_TITLES[code] ?? code.charAt(0).toUpperCase() + code.slice(1).replaceAll('_', ' ')
	);
}

/** Estimated downtime in words ("about 2 min"). */
export function downtimeText(seconds: number): string {
	if (seconds <= 0) return 'No downtime expected';
	if (seconds < 60) return `About ${Math.max(1, Math.round(seconds))} s`;
	const m = Math.round(seconds / 60);
	if (m < 90) return `About ${m} min`;
	return `About ${Math.round(m / 60)} h`;
}

/** Whether the transfer fits the destination's free space (-1 = unknown). */
export function spaceCheck(data: Schema<'MigrationData'>): 'ok' | 'short' | 'unknown' {
	const free = [data.destinationStacksFree, data.destinationVolumesFree];
	if (free.some((f) => f < 0)) return 'unknown';
	const need = data.projectBytes + data.volumeBytes;
	return Math.min(...free) >= need ? 'ok' : 'short';
}

const JOB_KINDS: Record<string, string> = {
	'stack.deploy': 'Deploy',
	'stack.start': 'Start',
	'stack.stop': 'Stop',
	'stack.restart': 'Restart',
	'stack.down': 'Take down',
	'stack.remove': 'Delete',
	'stack.build': 'Build images',
	'stack.migrate': 'Migrate',
	'stack.remove_source': 'Remove from source',
	'update.check': 'Update check',
	'update.run': 'Update',
	'backup.run': 'Backup',
	'backup.restore': 'Restore from backup'
};

/** Plain-language name of a job kind ("stack.deploy" → "Deploy"). */
export function jobKindLabel(kind: string): string {
	if (JOB_KINDS[kind]) return JOB_KINDS[kind];
	const s = kind.replaceAll('.', ' ').replaceAll('_', ' ');
	return s.charAt(0).toUpperCase() + s.slice(1);
}

const AUDIT_ACTIONS: Record<string, string> = {
	'stack.definition.read': 'Opened the definition',
	'stack.definition.write': 'Changed the definition',
	'stack.manage': 'Edited details',
	'stack.create': 'Created',
	'stack.import': 'Imported',
	'job.queued': 'Job queued',
	'job.started': 'Job started',
	'job.finished': 'Job finished',
	'job.cancel_requested': 'Cancellation requested'
};

/** Plain-language audit action ("stack.deploy" → "Deploy"). */
export function auditActionLabel(action: string): string {
	const s =
		AUDIT_ACTIONS[action] ??
		JOB_KINDS[action] ??
		action.replaceAll('.', ' ').replaceAll('_', ' ');
	return s.charAt(0).toUpperCase() + s.slice(1);
}

/**
 * Service names with their dependencies first (depends_on), stable in
 * definition order; cycles and unknown services keep definition order.
 */
export function dependencyOrder(
	services: { name: string; dependsOn?: { service: string }[] }[]
): string[] {
	const byName = new Map(services.map((s) => [s.name, s]));
	const out: string[] = [];
	const state = new Map<string, 'visiting' | 'done'>();
	const visit = (name: string) => {
		if (state.get(name)) return;
		state.set(name, 'visiting');
		for (const d of byName.get(name)?.dependsOn ?? [])
			if (byName.has(d.service)) visit(d.service);
		state.set(name, 'done');
		out.push(name);
	};
	for (const s of services) visit(s.name);
	return out;
}

/** The update states shown as "update available" on the stack (#20). */
export function updateAvailable(images: Schema<'StackImageStatus'>[] | undefined): boolean {
	return !!images?.some((i) => i.update === 'update_available');
}
