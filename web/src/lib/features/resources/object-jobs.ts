// The running jobs of the Docker object pages (docs/internal/web.md, "Job
// progress after reload"): what each detail and list page matches in the
// caller's running jobs (activeJobsQuery), so a reload or coming back shows
// the object's jobs again. Targets as the server sets them
// (docs/internal/architecture/job-engine.md): containers, volumes and
// networks by name, images by ID (removal) or by the reference that was
// pulled or built. Pure; tested in object-jobs.spec.ts.
import type { Job } from '$lib/api/client';
import type { JobMatch } from '$lib/features/jobs/active';

/** Quick read-only checks: no progress bar on an object's page. */
const CHECKS = ['update.check'];

/** File manager jobs (shown on the Files tab). */
export const FILE_JOB_KINDS = [
	'files.archive',
	'files.copy',
	'files.delete',
	'files.extract',
	'files.metadata',
	'files.move'
];

/** Jobs acting on a container (lifecycle, settings, updates, restores). */
export function containerJobs(env: string, name: string): JobMatch {
	return {
		targets: [{ type: 'container', id: name, environmentId: env }],
		excludeKinds: CHECKS
	};
}

/** Jobs acting on a network (create, remove). */
export function networkJobs(env: string, name: string): JobMatch {
	return { targets: [{ type: 'network', id: name, environmentId: env }] };
}

/** The volume page's tabs with their own job progress. */
export type VolumeTab = 'files' | 'backups' | 'migrate';

/** The tab a volume page path is on (`/volumes/e1/data/files` → files). */
export function volumeTab(pathname: string): VolumeTab | undefined {
	const last = pathname.replace(/\/+$/, '').split('/').pop();
	return last === 'files' || last === 'backups' || last === 'migrate' ? last : undefined;
}

/**
 * Jobs acting on a volume, shown above its tabs. The Files and Migrate
 * tabs' own jobs are left out while they are open (they show them
 * themselves: no second bar); the Backups tab shows no jobs, so its
 * backups and restores stay here.
 */
export function volumeJobs(env: string, name: string, tab?: VolumeTab): JobMatch {
	const own = tab === 'files' ? FILE_JOB_KINDS : tab === 'migrate' ? ['volume.migrate'] : [];
	return {
		targets: [{ type: 'volume', id: name, environmentId: env }],
		excludeKinds: [...CHECKS, ...own]
	};
}

/** The migrations of a volume (the migrate tab). */
export function volumeMigrationJobs(env: string, name: string): JobMatch {
	return {
		kinds: ['volume.migrate'],
		targets: [{ type: 'volume', id: name, environmentId: env }]
	};
}

/**
 * Where a volume migration copies to: its target volume in another
 * environment (the source is the job's own environment).
 */
export function migrationCopy(
	job: Pick<Job, 'environmentId' | 'targets'> | undefined
): { environmentId: string; name: string } | undefined {
	const source = job?.environmentId;
	const t = job?.targets?.find(
		(t) => t.type === 'volume' && !!t.environmentId && t.environmentId !== source
	);
	return t?.environmentId ? { environmentId: t.environmentId, name: t.id } : undefined;
}

function isRegistry(part: string): boolean {
	return part.includes('.') || part.includes(':') || part === 'localhost';
}

/**
 * The references a pull or build of `ref` may have named: Docker lists
 * `nginx:latest` for a pull of `nginx`, `docker.io/library/nginx` or
 * `library/nginx:latest`. Digest references stay as they are.
 */
export function imageReferences(ref: string): string[] {
	if (!ref || ref.includes('<none>')) return [];
	if (ref.includes('@')) return [ref];
	const slash = ref.lastIndexOf('/');
	const colon = ref.lastIndexOf(':');
	const repo = colon > slash ? ref.slice(0, colon) : ref;
	const tag = colon > slash ? ref.slice(colon + 1) : 'latest';
	const repos = [repo];
	const first = repo.split('/')[0];
	if (!repo.includes('/')) repos.push(`library/${repo}`, `docker.io/library/${repo}`);
	else if (!isRegistry(first)) repos.push(`docker.io/${repo}`);
	const tags = tag === 'latest' ? [':latest', ''] : [`:${tag}`];
	return repos.flatMap((r) => tags.map((t) => r + t));
}

/** Jobs acting on an image: removal (by ID), pulls and builds of its tags. */
export function imageJobs(
	env: string,
	im: { id: string; repoTags?: readonly string[]; repoDigests?: readonly string[] }
): JobMatch {
	const ids = new Set([
		im.id,
		...(im.repoTags ?? []).flatMap(imageReferences),
		...(im.repoDigests ?? [])
	]);
	return { targets: [...ids].map((id) => ({ type: 'image', id, environmentId: env })) };
}

/** Jobs of these kinds in the selected environment (null: every environment). */
export function kindJobs(kinds: readonly string[], env: string | null | undefined): JobMatch {
	return env ? { kinds, environmentId: env } : { kinds };
}
