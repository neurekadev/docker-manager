// Stack archives view model (#313): exporting a stack as one tar.gz (its
// project folder and the data of its plain local volumes) and creating a
// stack from such an archive. What the export dialog offers per volume and
// sends, the check's result in one line, the newest archive in words, and
// for creating: the default name, the request body and the volumes whose
// names change with the stack's. Pure; tested in archives.spec.ts.
import type { Job, Schema } from '$lib/api/client';
import { formatBytes, formatDateTime } from '$lib/ui/format';
import { asSentence } from './importing';
import { count } from './migration';
import { downtimeText, nameError } from './model';

export type StackExportPreview = Schema<'StackExportPreview'>;
export type StackArchiveVolume = Schema<'StackArchiveVolume'>;
export type StackExportFile = Schema<'StackExportFile'>;
export type StackArchiveExclusion = Schema<'StackArchiveExclusion'>;
export type StackArchive = Schema<'StackArchive'>;
export type StackArchiveImportPreview = Schema<'StackArchiveImportPreview'>;
export type StackArchiveImportVolume = Schema<'StackArchiveImportVolume'>;

const e = encodeURIComponent;

/** Where an export's archive downloads from (its ID is the job's). */
export function exportDownloadUrl(stackId: string, exportId: string): string {
	return `/api/v1/stacks/${e(stackId)}/exports/${e(exportId)}`;
}

/** The request body of an export check or an export leaving out `excluded`. */
export function exportBody(excluded: readonly string[]): Schema<'StackExportBody'> {
	return excluded.length ? { excludeVolumes: [...new Set(excluded)].sort() } : {};
}

/** A key of the volumes left out; the order of the ticks does not matter. */
export function exportKey(excluded: readonly string[]): string {
	return JSON.stringify([...new Set(excluded)].sort());
}

/**
 * The volumes the user may not download (volume.files.download): left
 * out from the start, so the export can run without them.
 */
export function notPermittedKeys(volumes: readonly StackArchiveVolume[]): string[] {
	return volumes.filter((v) => v.notPermitted).map((v) => v.key);
}

/** What the Include tick of a volume shows. */
export interface IncludeChoice {
	checked: boolean;
	disabled: boolean;
	/** Why it cannot be included (a sentence). */
	reason?: string;
}

export const NOT_PERMITTED = "You can't download this volume's files.";

/**
 * A volume's Include tick: the user's choice for a plain local volume;
 * off and fixed for one they may not download or one whose data cannot
 * go into an archive (external, not created yet, another driver).
 */
export function includeChoice(v: StackArchiveVolume, excluded: readonly string[]): IncludeChoice {
	if (v.notPermitted) return { checked: false, disabled: true, reason: NOT_PERMITTED };
	if (v.reason) return { checked: false, disabled: true, reason: asSentence(v.reason) };
	return { checked: !excluded.includes(v.key), disabled: false };
}

/** The one-line result of a check: can it run, and with how many warnings. */
export function archiveHeadline(
	p: { blockers: readonly unknown[]; warnings: readonly unknown[] },
	what: 'export' | 'create'
): { tone: 'danger' | 'warn' | 'info'; title: string } {
	const b = p.blockers.length;
	const w = p.warnings.length;
	const ready = what === 'export' ? 'Ready to export' : 'Ready to create the stack';
	if (b)
		return {
			tone: 'danger',
			title:
				what === 'export'
					? `${count(b, 'problem')} must be fixed before the stack can be exported.`
					: `${count(b, 'problem')} must be fixed before the stack can be created.`
		};
	if (w) return { tone: 'warn', title: `${ready}, with ${count(w, 'warning')}.` };
	return { tone: 'info', title: `${ready}.` };
}

/**
 * The downtime of an export: the running services stop while the archive
 * is written. Null when nothing runs.
 */
export function exportDowntime(
	title: string,
	p: Pick<StackExportPreview, 'running' | 'downtimeSeconds'>
): { title: string; body: string } | null {
	if (!p.running.length) return null;
	return {
		title: `${title} stops while the archive is written and starts again afterwards.`,
		body: `Expected downtime: ${downtimeText(p.downtimeSeconds).toLowerCase()}.`
	};
}

/**
 * Whether the archive limit is worth showing: the data comes close to it
 * (80 %) or exceeds it.
 */
export function limitRelevant(p: Pick<StackExportPreview, 'totalBytes' | 'maxBytes'>): boolean {
	return p.maxBytes > 0 && p.totalBytes >= p.maxBytes * 0.8;
}

/** "web-2026-10-10.tar.gz · 1.2 GB · Available until Oct 11, 2026, 09:00". */
export function archiveFileText(
	f: Pick<StackExportFile, 'fileName' | 'size' | 'expiresAt'>,
	timeZone?: string
): string {
	return `${f.fileName} · ${formatBytes(f.size)} · Available until ${formatDateTime(f.expiresAt, timeZone)}`;
}

const EXCLUSION_KINDS: Record<StackArchiveExclusion['kind'], string> = {
	volume: 'Volume',
	anonymous_volume: 'Anonymous volume',
	bind: 'Bind mount'
};

/** What was left out, in words: "Bind mount /srv/media: outside the project folder." */
export function exclusionText(x: Pick<StackArchiveExclusion, 'kind' | 'name' | 'reason'>): {
	what: string;
	reason: string;
} {
	return { what: EXCLUSION_KINDS[x.kind] ?? 'Item', reason: asSentence(x.reason) };
}

/** The stack name a new stack from the archive starts with. */
export function defaultName(a: Pick<StackArchive, 'name' | 'pinnedName'>): string {
	return a.pinnedName || a.name;
}

/** The data an archive holds: its project folder and volumes. */
export function archiveDataBytes(a: Pick<StackArchive, 'projectBytes' | 'volumes'>): number {
	return a.projectBytes + a.volumes.reduce((n, v) => n + v.bytes, 0);
}

/** "2 volumes · 1.5 GB". */
export function archiveContents(a: Pick<StackArchive, 'projectBytes' | 'volumes'>): string {
	return `${count(a.volumes.length, 'volume')} · ${formatBytes(archiveDataBytes(a))}`;
}

/** What the user chose for a stack from an archive. */
export interface ImportChoice {
	environmentId: string;
	name: string;
	displayName: string;
	deploy: boolean;
}

/** The request body of an import check or import for a choice. */
export function importBody(c: ImportChoice): Schema<'StackArchiveImportBody'> {
	const display = c.displayName.trim();
	return {
		environmentId: c.environmentId,
		name: c.name.trim(),
		displayName: display || undefined,
		deploy: c.deploy || undefined
	};
}

/** A key of what the import check depends on. */
export function importKey(archiveId: string, c: ImportChoice): string {
	return JSON.stringify([archiveId, c.environmentId, c.name.trim(), c.deploy]);
}

/** The volumes whose names change with the new stack's name. */
export function renamedVolumes(
	volumes: readonly StackArchiveImportVolume[]
): StackArchiveImportVolume[] {
	return volumes.filter((v) => v.source !== v.name);
}

/** What keeps a check's button off: a check in flight, or its problems. */
function checkBlocker(
	c: {
		preview: { allowed: boolean; blockers: readonly unknown[] } | null;
		checking: boolean;
		stale: boolean;
	},
	what: string
): string | undefined {
	if (c.checking || c.stale || !c.preview) return 'Checking…';
	if (!c.preview.allowed)
		return c.preview.blockers.length
			? `Fix ${c.preview.blockers.length === 1 ? 'the problem' : 'the problems'} first.`
			: `The stack cannot be ${what} now.`;
	return undefined;
}

/** Why Export Archive is off, or undefined when the export can start. */
export function exportBlocker(c: Parameters<typeof checkBlocker>[0]): string | undefined {
	return checkBlocker(c, 'exported');
}

/** Why Create Stack is off, or undefined when the stack can be created. */
export function importBlocker(
	c: Parameters<typeof checkBlocker>[0] & {
		environment?: { name: string; online: boolean };
		name: string;
	}
): string | undefined {
	if (!c.environment) return 'Choose an environment for the stack.';
	if (nameError(c.name)) return 'Fix the name to continue.';
	if (!c.environment.online)
		return `${c.environment.name} is offline. Create the stack when it is back.`;
	return checkBlocker(c, 'created');
}

/**
 * How a finished import ended: the stack was created (a deploy asked for
 * runs as a job of its own), created but its deploy was refused
 * (`deploy_failed`: the stack and its volumes are kept, the upload is
 * gone), or nothing was kept (the upload stays for another try).
 */
export function importOutcome(
	job: Pick<Job, 'state'> & { error?: Pick<NonNullable<Job['error']>, 'class'> }
): 'created' | 'deploy_failed' | 'failed' {
	if (job.state === 'succeeded') return 'created';
	return job.error?.class === 'deploy_failed' ? 'deploy_failed' : 'failed';
}

/** The caller's own running job (never another user's in the same environment). */
export function ownJob<J extends Pick<Job, 'id' | 'initiatorUserId'>>(
	jobs: readonly J[],
	userId: string | undefined,
	skip: ReadonlySet<string> = new Set()
): J | undefined {
	if (!userId) return undefined;
	return jobs.find((j) => j.initiatorUserId === userId && !skip.has(j.id));
}
