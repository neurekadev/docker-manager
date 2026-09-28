// Pure helpers of the Docker resource pages (#6): volume file access,
// port and mount text, the create-form parsers. Unit-tested in
// model.spec.ts.
import type { Schema } from '$lib/api/client';

type Volume = Schema<'Volume'>;
type Port = Schema<'ContainerPort'>;

/** Local-driver mount types that point at remote storage (mirrors the agent, #28). */
const REMOTE_TYPES = ['nfs', 'nfs4', 'cifs', 'smb', 'smb3', 'sshfs', 'glusterfs', 'ceph'];

export interface VolumeAccess {
	/** File access, watching and backup work (a local volume on the host). */
	local: boolean;
	/** Why the volume is read-only in Docker Manager (shown next to it). */
	reason?: string;
}

/**
 * Whether Docker Manager can open a volume's files (#28, docs/internal/support-matrix.md):
 * only local-driver volumes on the host; plugins and local volumes backed by
 * NFS/CIFS mount options are listed read-only. The agent decides again.
 */
export function volumeAccess(v: Pick<Volume, 'driver' | 'options'>): VolumeAccess {
	if (!v.driver) return { local: true };
	if (v.driver !== 'local')
		return {
			local: false,
			reason: `Volume driver "${v.driver}": only local volumes support files, watching and backups.`
		};
	const type = (v.options?.type ?? '').toLowerCase();
	if (REMOTE_TYPES.includes(type) || (v.options?.o ?? '').includes('addr='))
		return {
			local: false,
			reason: `Backed by remote storage (${type || 'network mount'}): files, watching and backups are not supported.`
		};
	return { local: true };
}

/** "8080:80/tcp", "127.0.0.1:53:53/udp", "80/tcp" (exposed only). */
export function portText(p: Port): string {
	const proto = p.protocol && p.protocol !== 'tcp' ? `/${p.protocol}` : '';
	if (!p.hostPort) return `${p.containerPort}${proto || '/tcp'}`;
	const ip = p.hostIp && p.hostIp !== '0.0.0.0' && p.hostIp !== '::' ? `${p.hostIp}:` : '';
	return `${ip}${p.hostPort}:${p.containerPort}${proto}`;
}

/** Published ports without the IPv4/IPv6 duplicates the Engine reports. */
export function uniquePorts(ports: readonly Port[] | undefined): Port[] {
	const seen = new Set<string>();
	const out: Port[] = [];
	for (const p of ports ?? []) {
		const k = `${p.hostPort ?? 0}:${p.containerPort}/${p.protocol}`;
		if (seen.has(k)) continue;
		seen.add(k);
		out.push(p);
	}
	return out.sort(
		(a, b) => (a.hostPort ?? 0) - (b.hostPort ?? 0) || a.containerPort - b.containerPort
	);
}

/** A link to a published port when the environment has a service address (#3). */
export function portHref(p: Port, serviceAddress: string | undefined): string | undefined {
	if (!serviceAddress || !p.hostPort || p.protocol === 'udp') return undefined;
	if (p.hostIp && (p.hostIp.startsWith('127.') || p.hostIp === '::1')) return undefined;
	const https = p.containerPort === 443 || p.hostPort === 443;
	return `${https ? 'https' : 'http'}://${serviceAddress}:${p.hostPort}`;
}

/** Non-empty trimmed lines. */
export function lines(text: string): string[] {
	return text
		.split(/\r?\n/)
		.map((l) => l.trim())
		.filter((l) => l !== '' && !l.startsWith('#'));
}

export interface ParsedPairs {
	values: Record<string, string>;
	/** 1-based line numbers without a KEY=value shape. */
	invalid: number[];
}

/** KEY=value lines (labels, build args, driver options). Blank lines and # comments are skipped. */
export function parsePairs(text: string): ParsedPairs {
	const values: Record<string, string> = {};
	const invalid: number[] = [];
	text.split(/\r?\n/).forEach((raw, i) => {
		const l = raw.trim();
		if (l === '' || l.startsWith('#')) return;
		const eq = l.indexOf('=');
		if (eq <= 0) {
			invalid.push(i + 1);
			return;
		}
		values[l.slice(0, eq).trim()] = l.slice(eq + 1);
	});
	return { values, invalid };
}

/** KEY=value lines as a list (container environment keeps order and duplicates). */
export function envLines(text: string): { values: string[]; invalid: number[] } {
	const values: string[] = [];
	const invalid: number[] = [];
	text.split(/\r?\n/).forEach((raw, i) => {
		const l = raw.replace(/^\s+/, '');
		if (l.trim() === '' || l.startsWith('#')) return;
		if (l.indexOf('=') <= 0) invalid.push(i + 1);
		else values.push(l);
	});
	return { values, invalid };
}

/**
 * Splits a command line into arguments like a POSIX shell would for plain
 * words and quotes (no expansion): `sh -c "echo hi"` → [sh, -c, echo hi].
 * Returns null for an unterminated quote.
 */
export function splitCommand(text: string): string[] | null {
	const out: string[] = [];
	let cur = '';
	let has = false;
	let quote: '"' | "'" | null = null;
	for (let i = 0; i < text.length; i++) {
		const ch = text[i];
		if (quote) {
			if (ch === quote) quote = null;
			else if (ch === '\\' && quote === '"' && i + 1 < text.length) cur += text[++i];
			else cur += ch;
			continue;
		}
		if (ch === '"' || ch === "'") {
			quote = ch;
			has = true;
		} else if (ch === '\\' && i + 1 < text.length) {
			cur += text[++i];
			has = true;
		} else if (/\s/.test(ch)) {
			if (has) out.push(cur);
			cur = '';
			has = false;
		} else {
			cur += ch;
			has = true;
		}
	}
	if (quote) return null;
	if (has) out.push(cur);
	return out;
}

/** Joins arguments back for display, quoting those with spaces. */
export function joinCommand(args: readonly string[] | undefined): string {
	return (args ?? [])
		.map((a) => (a === '' || /[\s"'\\]/.test(a) ? JSON.stringify(a) : a))
		.join(' ');
}

/** "sha256:4e1b5f1a6d8e…" → "4e1b5f1a6d8e". */
export function shortDigest(id: string | undefined, n = 12): string {
	if (!id) return '';
	const hex = id.includes(':') ? id.slice(id.indexOf(':') + 1) : id;
	return hex.slice(0, n);
}

/** Memory text field (MB) → bytes; "" → undefined; invalid → NaN. */
export function megabytes(text: string): number | undefined {
	const t = text.trim();
	if (t === '') return undefined;
	const n = Number(t);
	return Number.isFinite(n) && n >= 0 ? Math.round(n * 1024 * 1024) : NaN;
}

/** Seconds → a compact "1h 5m" / "42s" (durations of builds). */
export function compactDuration(ms: number | undefined): string {
	if (ms === undefined || ms < 0) return '—';
	const s = Math.round(ms / 1000);
	if (s < 60) return `${s}s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m}m ${s % 60}s`;
	return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/** A container's network endpoint as the API reports it. */
export interface NetworkAddresses {
	name: string;
	ipAddress?: string;
	ipv6Address?: string;
}

/** One network of one or more containers with their addresses on it. */
export interface NetworkEntry {
	name: string;
	/** IPv4 before IPv6, without duplicates; empty for host networking or while stopped. */
	addresses: string[];
}

/**
 * The networks of one or more containers (a service's replicas), in
 * network order, each once with every address on it.
 */
export function networkEntries(
	networks: readonly (readonly NetworkAddresses[] | undefined)[]
): NetworkEntry[] {
	const byName = new Map<string, NetworkEntry>();
	for (const list of networks)
		for (const n of list ?? []) {
			if (!n.name) continue;
			let e = byName.get(n.name);
			if (!e) byName.set(n.name, (e = { name: n.name, addresses: [] }));
			for (const a of [n.ipAddress, n.ipv6Address])
				if (a && !e.addresses.includes(a)) e.addresses.push(a);
		}
	for (const e of byName.values())
		e.addresses.sort((a, b) => Number(a.includes(':')) - Number(b.includes(':')));
	return [...byName.values()];
}

/** Started while in these states: the uptime counts from startedAt. */
const UP_STATES = new Set(['running', 'paused', 'restarting']);

/** A container's live start time (for its uptime), or null when it is not up. */
export function upSince(c: { state: string; startedAt?: string }): string | null {
	return UP_STATES.has(c.state) && c.startedAt ? c.startedAt : null;
}

/** Sort key for an uptime column: longer uptimes sort after shorter ones. */
export function uptimeSortValue(since: string | null | undefined): number | null {
	if (!since) return null;
	const t = Date.parse(since);
	return Number.isFinite(t) ? -t : null;
}

/** The status a container shows: its health while running, else its state. */
export function containerStatus(c: { state: string; health?: string }): string {
	if (c.state === 'running' && (c.health === 'unhealthy' || c.health === 'starting'))
		return c.health;
	return c.state;
}

/**
 * The short form Docker lists references in: "docker.io/library/nginx" →
 * "nginx", "docker.io/acme/app" → "acme/app", and ":latest" when no tag or
 * digest is given.
 */
export function normalizeReference(ref: string): string {
	let r = ref.trim();
	if (!r) return r;
	for (const p of [
		'docker.io/library/',
		'index.docker.io/library/',
		'docker.io/',
		'index.docker.io/'
	])
		if (r.startsWith(p)) {
			r = r.slice(p.length);
			break;
		}
	if (r.includes('@')) return r;
	const lastSlash = r.lastIndexOf('/');
	return r.slice(lastSlash + 1).includes(':') ? r : `${r}:latest`;
}

/**
 * Whether an image the create form names is already on the environment
 * (#25: creating a container never pulls). Accepts references and image
 * IDs (sha256:… or a 12+ digit prefix).
 */
export function imagePresent(
	ref: string,
	images: readonly { id: string; repoTags: string[]; repoDigests?: string[] }[]
): boolean {
	const r = ref.trim();
	if (!r) return false;
	const hex = r.startsWith('sha256:') ? r.slice(7) : r;
	if (/^[a-f0-9]{12,64}$/.test(hex))
		return images.some((i) => shortDigest(i.id, 64).startsWith(hex));
	const want = normalizeReference(r);
	return images.some(
		(i) =>
			i.repoTags.some((t) => normalizeReference(t) === want) ||
			(want.includes('@') &&
				(i.repoDigests ?? []).some((d) => normalizeReference(d) === want))
	);
}

/** A Docker object name (containers, volumes, networks). */
export const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/;

// Detail pages (#22 polish): restart policies and health in words, the
// command of a health check, labels split into the user's and the
// system's, network aliases without the noise Docker adds, images with
// and without tags, and columns that would repeat one value on every row.

/** Restart policy options of the create and edit forms, in words. */
export const RESTART_OPTIONS = [
	{ value: 'no', label: 'Never restart' },
	{ value: 'on-failure', label: 'On failure' },
	{ value: 'unless-stopped', label: 'Unless stopped' },
	{ value: 'always', label: 'Always' }
] as const;

/**
 * A restart policy in words: "Never restart", "Unless stopped", "Always",
 * "On failure (up to 5 retries)", "On failure" when unlimited or not
 * reported. `maxRetries` is the container's `restartMaxRetries`; a name
 * carrying the retries ("on-failure:5") is understood too.
 */
export function restartPolicyLabel(policy: string | undefined, maxRetries?: number): string {
	const [name, inline] = (policy ?? '').split(':');
	const retries = maxRetries || (inline ? Number(inline) : undefined);
	switch (name) {
		case '':
		case 'no':
			return 'Never restart';
		case 'always':
			return 'Always';
		case 'unless-stopped':
			return 'Unless stopped';
		case 'on-failure':
			return retries && retries > 0
				? `On failure (up to ${retries} ${retries === 1 ? 'retry' : 'retries'})`
				: 'On failure';
	}
	return name;
}

/** A container's health in words ("No health check" without one). */
export function healthLabel(health: string | undefined): string {
	switch (health) {
		case 'healthy':
			return 'Healthy';
		case 'unhealthy':
			return 'Unhealthy';
		case 'starting':
			return 'Starting';
	}
	return 'No health check';
}

/**
 * The command a health check runs, without Docker's CMD / CMD-SHELL
 * marker: ["CMD-SHELL", "curl -f http://localhost/"] → "curl -f
 * http://localhost/". Empty for none or ["NONE"].
 */
export function healthCommand(test: readonly string[] | undefined): string {
	if (!test?.length || test[0] === 'NONE') return '';
	if (test[0] === 'CMD-SHELL') return test.slice(1).join(' ');
	if (test[0] === 'CMD') return joinCommand(test.slice(1));
	return joinCommand(test);
}

/** Label prefixes Docker, Compose, image builders and Docker Manager set themselves. */
const SYSTEM_LABEL_PREFIXES = [
	'com.docker.',
	'org.opencontainers.',
	'dev.neureka.docker-manager.',
	'desktop.docker.io/'
];

/** Whether a label was set by Docker, Compose, an image build or Docker Manager. */
export function isSystemLabel(key: string): boolean {
	return SYSTEM_LABEL_PREFIXES.some((p) => key.startsWith(p));
}

export interface LabelGroups {
	/** Labels someone chose (shown), sorted by key. */
	user: [string, string][];
	/** Labels set by Docker, Compose or image builders (folded away), sorted by key. */
	system: [string, string][];
}

/** Splits labels into the user's and the system's, each sorted by key. */
export function splitLabels(labels: Record<string, string> | undefined): LabelGroups {
	const out: LabelGroups = { user: [], system: [] };
	for (const e of Object.entries(labels ?? {}).sort(([a], [b]) => a.localeCompare(b)))
		(isSystemLabel(e[0]) ? out.system : out.user).push(e);
	return out;
}

/**
 * The aliases worth showing for a container on a network: each once,
 * without the container's own name, hostname or ID prefix (Docker adds
 * those itself).
 */
export function networkAliases(
	aliases: readonly string[] | undefined,
	c: { name: string; id: string; hostname?: string }
): string[] {
	const out: string[] = [];
	for (const a of aliases ?? []) {
		if (!a || out.includes(a) || a === c.name || a === c.hostname) continue;
		if (a.length >= 12 && c.id.startsWith(a)) continue;
		out.push(a);
	}
	return out;
}

/** Whether the hostname is only the container ID's prefix (Docker's default). */
export function hostnameIsId(hostname: string | undefined, id: string): boolean {
	return !!hostname && hostname.length >= 12 && id.startsWith(hostname);
}

/** Images with a tag (the list's main rows) and untagged ones (folded away at the end). */
export function splitUntagged<T extends { repoTags: readonly string[] }>(
	images: readonly T[]
): { tagged: T[]; untagged: T[] } {
	const tagged: T[] = [];
	const untagged: T[] = [];
	for (const im of images) (im.repoTags.length ? tagged : untagged).push(im);
	return { tagged, untagged };
}

/**
 * Whether a column would repeat one value on every row (every volume's
 * driver is "local"): such columns are hidden, like filters that offer one
 * choice. Missing values count as their own value.
 */
export function sameEverywhere<T>(rows: readonly T[], value: (row: T) => unknown): boolean {
	if (rows.length === 0) return true;
	const first = value(rows[0]);
	return rows.every((r) => value(r) === first);
}

/** A container attached to a network, with its addresses on it. */
export interface AttachedContainer {
	id: string;
	name: string;
	state?: string;
	/** IPv4 before IPv6; empty while stopped or when the containers are not readable. */
	addresses: string[];
}

/** A network's attached container as the network's answer names it. */
export interface NetworkContainerRef {
	id: string;
	name: string;
	state?: string;
	ipAddress?: string;
	ipv6Address?: string;
}

/**
 * The containers attached to a network, sorted by name, each with its
 * addresses on that network: those the network's answer reports, else
 * (older agents report none) the container's endpoint in the containers
 * list.
 */
export function attachedContainers(
	network: string,
	refs: readonly NetworkContainerRef[],
	containers: readonly { name: string; networks?: readonly NetworkAddresses[] }[] = []
): AttachedContainer[] {
	const byName = new Map(containers.map((c) => [c.name, c]));
	return refs
		.map(({ ipAddress, ipv6Address, ...r }) => {
			const n =
				ipAddress || ipv6Address
					? { ipAddress, ipv6Address }
					: byName.get(r.name)?.networks?.find((x) => x.name === network);
			return {
				...r,
				addresses: [n?.ipAddress, n?.ipv6Address].filter((a): a is string => !!a)
			};
		})
		.sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * Whether a network's answer lacks an attached container's addresses, so
 * the page needs the containers list for them (older agents).
 */
export function needsAddressLookup(refs: readonly NetworkContainerRef[] | undefined): boolean {
	return !!refs?.some((r) => !r.ipAddress && !r.ipv6Address);
}
