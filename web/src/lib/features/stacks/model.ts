// Stack view model (#22 track B2, #7): pure functions that turn API DTOs
// into what the stack pages show. No Svelte, no fetch: tested in
// model.spec.ts.
import type { MyPermissions, Schema } from '$lib/api/client';
import { networkEntries, type NetworkEntry } from '$lib/features/resources/model';
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

/** The networks of a service's containers, each with their addresses (IPv4 first). */
export function serviceNetworks(svc: StackServiceStatus): NetworkEntry[] {
	return networkEntries(svc.containers.map((c) => c.networks));
}

export interface VolumeEntry {
	name: string;
	/** Created by the Engine for an anonymous mount (the name is a random ID). */
	anonymous: boolean;
	/** Where the service's containers mount it. */
	destinations: string[];
	/** Every mount of it is read-only. */
	readOnly: boolean;
}

/**
 * The volumes of a service's containers, each once (replicas share named
 * volumes): named volumes first, then anonymous ones, each in mount order.
 * Bind mounts are not volumes and never listed.
 */
export function serviceVolumes(svc: StackServiceStatus): VolumeEntry[] {
	const byName = new Map<string, VolumeEntry>();
	for (const c of svc.containers)
		for (const v of c.volumes ?? []) {
			if (!v.name) continue;
			let e = byName.get(v.name);
			if (!e)
				byName.set(
					v.name,
					(e = {
						name: v.name,
						anonymous: !!v.anonymous,
						destinations: [],
						readOnly: true
					})
				);
			if (v.destination && !e.destinations.includes(v.destination))
				e.destinations.push(v.destination);
			if (!v.readOnly) e.readOnly = false;
		}
	const all = [...byName.values()];
	return [...all.filter((v) => !v.anonymous), ...all.filter((v) => v.anonymous)];
}

/** A volume in words, for tooltips: "shop_data at /data (read-only)". */
export function volumeText(v: VolumeEntry): string {
	const name = v.anonymous ? `Anonymous volume ${v.name}` : v.name;
	const at = v.destinations.length ? ` at ${v.destinations.join(', ')}` : '';
	return `${name}${at}${v.readOnly ? ' (read-only)' : ''}`;
}

/**
 * The image ID of a service for a link to its page: the image a running
 * container runs, else any container's, else the one the last deploy
 * applied (undefined when none is known).
 */
export function serviceImageId(svc: StackServiceStatus): string | undefined {
	const running = svc.containers.find((c) => c.state === 'running' && c.imageId);
	if (running) return running.imageId;
	const any = svc.containers.find((c) => c.imageId);
	if (any) return any.imageId;
	return svc.applied?.imageId || undefined;
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

/** A container's newest sample (GET …/metrics/containers). */
export interface LatestSample {
	container: string;
	cpuPercent?: number;
	memoryUsedBytes?: number;
}

/**
 * Sums the containers' CPU series per timestamp (a stack uses the sum of
 * its containers' share of the environment's cores, #5) and takes each
 * container's latest CPU and memory. With `latest` (the current values of
 * the stack's containers: live, or the newest 10 s samples) they come from there
 * instead of the last minute bucket; containers without a recent sample
 * (stopped) then have no current value.
 */
export function stackUsage(metrics: ContainerMetrics[], latest?: LatestSample[]): StackUsage {
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
	let cpuNow = metrics.length ? lastValue(cpu) : null;
	if (latest) {
		for (const k of Object.keys(containers)) delete containers[k];
		for (const m of latest)
			containers[m.container] = {
				cpu: m.cpuPercent ?? null,
				memory: m.memoryUsedBytes ?? null
			};
		const cpus = latest.flatMap((m) => (m.cpuPercent === undefined ? [] : [m.cpuPercent]));
		cpuNow = cpus.length ? cpus.reduce((n, v) => n + v, 0) : null;
	}
	const mem = Object.values(containers).map((c) => c.memory);
	return {
		cpu,
		cpuNow,
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

/** Consecutive revisions with the same files (the same fingerprint), newest first. */
export interface RevisionGroup<R extends Pick<StackRevision, 'id' | 'hash'>> {
	/** The newest revision of the run. */
	head: R;
	/** Every revision of the run, newest first (head included). */
	members: R[];
}

/**
 * Groups a newest-first revision list into runs of the same fingerprint
 * (a deploy of unchanged files, a save that changed nothing): the history
 * shows one row per run and comparisons never pick two equal revisions.
 */
export function groupRevisions<R extends Pick<StackRevision, 'id' | 'hash'>>(
	list: R[]
): RevisionGroup<R>[] {
	const out: RevisionGroup<R>[] = [];
	for (const r of list) {
		const last = out.at(-1);
		if (last && last.head.hash === r.hash) last.members.push(r);
		else out.push({ head: r, members: [r] });
	}
	return out;
}

/**
 * The comparison the Revisions tab opens with: the deployed revision
 * against the files on disk while they differ, else the newest run against
 * the run before it; null when no two revisions differ.
 */
export function defaultComparison<R extends Pick<StackRevision, 'id' | 'hash'>>(
	list: R[],
	appliedId: string | undefined,
	diskId: string | undefined,
	undeployed: boolean
): { from: string; to: string } | null {
	const applied = list.find((r) => r.id === appliedId);
	const disk = list.find((r) => r.id === diskId);
	if (undeployed && applied && disk && applied.hash !== disk.hash)
		return { from: applied.id, to: disk.id };
	const groups = groupRevisions(list);
	return groups.length >= 2 ? { from: groups[1].head.id, to: groups[0].head.id } : null;
}

/**
 * What to compare a revision with: the deployed revision when its files
 * differ, else the next older revision with other files; null when none.
 */
export function comparisonFor<R extends Pick<StackRevision, 'id' | 'hash'>>(
	list: R[],
	r: R,
	appliedId: string | undefined
): { from: string; to: string } | null {
	const applied = list.find((x) => x.id === appliedId);
	if (applied && applied.hash !== r.hash) return { from: applied.id, to: r.id };
	const older = list.slice(list.indexOf(r) + 1).find((x) => x.hash !== r.hash);
	return older ? { from: older.id, to: r.id } : null;
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
	'stack.rename': 'Rename',
	'stack.pull': 'Pull images',
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
	'stack.rename.preview': 'Previewed a rename',
	'stack.validate': 'Validated the definition',
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

/**
 * The projects the Import project dialog lists (#7): those to import
 * first, then by name; hideManaged leaves out the ones Docker Manager
 * manages already, except those keep holds (an import started in the
 * dialog keeps showing its outcome).
 */
export function importCandidates<P extends { name: string; stackId?: string }>(
	projects: P[],
	hideManaged: boolean,
	keep: (p: P) => boolean = () => false
): P[] {
	return projects
		.filter((p) => !hideManaged || !p.stackId || keep(p))
		.sort((a, b) => Number(!!a.stackId) - Number(!!b.stackId) || a.name.localeCompare(b.name));
}

/** The update states shown as "update available" on the stack (#20). */
export function updateAvailable(images: Schema<'StackImageStatus'>[] | undefined): boolean {
	return pendingUpdates(images).length > 0;
}

/** A service whose image has a newer version (Pull & Deploy says so, #20). */
export interface PendingUpdate {
	service: string;
	/** The image reference with its tag, e.g. nginx:1.27 (never a digest). */
	image: string;
	/** The newer image is already on the host (pulled, not deployed yet). */
	pulled: boolean;
}

/**
 * Services with a newer image: the update check found one in the registry
 * (update_available), or a pull left one on the host that no deploy runs
 * yet. Build-only services are left out (a rebuild updates them).
 */
export function pendingUpdates(images: Schema<'StackImageStatus'>[] | undefined): PendingUpdate[] {
	return (images ?? [])
		.filter((i) => !i.build && (i.update === 'update_available' || !!i.pulledImageId))
		.map((i) => ({ service: i.service, image: i.image, pulled: !!i.pulledImageId }))
		.sort((a, b) => a.service.localeCompare(b.service));
}

// Deploys (#7): what a finished deploy did, and orphaned services.

/** The kind of deploy a button or dialog started. */
export interface DeployChoice {
	/** Pull every image first ("Pull"). */
	pull?: boolean;
	/** Rebuild every build section. */
	build?: boolean;
	/** Also remove the containers of services no longer in the Compose file. */
	removeOrphans?: boolean;
}

/** What runs while a deploy job is in the tray, e.g. "Pull Silo". */
export function deployTitle(title: string, c: DeployChoice): string {
	if (c.build) return `Build and deploy ${title}`;
	if (c.pull) return `Pull and deploy ${title}`;
	if (c.removeOrphans) return `Deploy ${title} and remove orphans`;
	return `Deploy ${title}`;
}

/** The failure toast title of a deploy. */
export function deployFailure(title: string, c: DeployChoice): string {
	return c.pull ? `${title} was not pulled and deployed` : `${title} was not deployed`;
}

/**
 * The success toast of a deploy. A deploy that started no container keeps
 * the stack's last deploy time (appliedRevision.at), so an unchanged time
 * means the Engine already ran the definition.
 */
export function deploySuccess(
	title: string,
	c: DeployChoice,
	before: string | undefined,
	after: string | undefined
): string {
	const unchanged = before === after;
	if (c.removeOrphans)
		return unchanged
			? `Removed the orphaned containers of ${title}; everything else already ran its definition`
			: `Deployed ${title} and removed its orphaned containers`;
	if (!unchanged) {
		if (c.build) return `Built and deployed ${title}`;
		if (c.pull) return `Pulled newer images and redeployed ${title}`;
		return `Deployed ${title}`;
	}
	if (c.pull) return `Nothing to update: ${title} already runs the newest images`;
	if (c.build) return `Built the images of ${title}; nothing needed to be redeployed`;
	return `Nothing to deploy: ${title} already runs its definition`;
}

/** Services with containers on the host that the deployed definition no longer has. */
export function orphanedServices(services: { name: string; drift: string[] }[] | undefined) {
	return (services ?? [])
		.filter((s) => s.drift.includes('unexpected_service'))
		.map((s) => s.name);
}

export interface DriftNote {
	service: string;
	text: string;
	/** An orphan: removed from the Compose file, container still on the host. */
	orphan: boolean;
}

const DRIFT_TEXT: Record<string, (s: string) => string> = {
	missing: (s) => `${s} has no container. Deploy the stack to create it.`,
	not_running: (s) => `${s} is not running. Start it, or deploy the stack.`,
	running_while_stopped: (s) =>
		`${s} runs although the stack was stopped. Stop the stack again, or start it to keep it running.`,
	unexpected_service: (s) =>
		`${s} is no longer in the Compose file, but its container is still on the host. “Remove old containers” deploys the stack and removes it.`,
	image_changed: (s) =>
		`${s} runs another image than the last deploy used. Deploy the stack to run the image its Compose file names.`
};

/** One plain sentence per drift finding, orphans first. */
export function driftNotes(services: { name: string; drift: string[] }[] | undefined): DriftNote[] {
	const out: DriftNote[] = [];
	for (const s of services ?? [])
		for (const d of s.drift)
			out.push({
				service: s.name,
				orphan: d === 'unexpected_service',
				text: DRIFT_TEXT[d]?.(s.name) ?? `${s.name}: ${d.replaceAll('_', ' ')}.`
			});
	return out.sort((a, b) => Number(b.orphan) - Number(a.orphan));
}
