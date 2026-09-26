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
 * Whether Docker Manager can open a volume's files (#28, docs/support-matrix.md):
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

/** The status a container shows: its health while running, else its state. */
export function containerStatus(c: { state: string; health?: string }): string {
	if (c.state === 'running' && (c.health === 'unhealthy' || c.health === 'starting'))
		return c.health;
	return c.state;
}

export interface ContainerFilter {
	q: string;
	state: string;
	/** '' all, '-' standalone only, else a Compose project. */
	stack: string;
	/** "key" or "key=value" (full view only). */
	label: string;
}

/** Applies the list filters (#6: state, stack, label, name/image text). */
export function filterContainers<
	T extends {
		name: string;
		image?: string;
		state: string;
		stack?: { project: string };
		labels?: Record<string, string>;
	}
>(rows: readonly T[], f: ContainerFilter): T[] {
	const q = f.q.trim().toLowerCase();
	const [lk, lv] = f.label.includes('=')
		? [
				f.label.slice(0, f.label.indexOf('=')).trim(),
				f.label.slice(f.label.indexOf('=') + 1).trim()
			]
		: [f.label.trim(), undefined];
	return rows.filter((c) => {
		if (q && !c.name.toLowerCase().includes(q) && !(c.image ?? '').toLowerCase().includes(q))
			return false;
		if (f.state && c.state !== f.state) return false;
		if (f.stack === '-' && c.stack) return false;
		if (f.stack && f.stack !== '-' && c.stack?.project !== f.stack) return false;
		if (lk) {
			const v = c.labels?.[lk];
			if (v === undefined || (lv !== undefined && v !== lv)) return false;
		}
		return true;
	});
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
