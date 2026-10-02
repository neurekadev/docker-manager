// Which running jobs a backup view shows (docs/internal/web.md, "Job
// progress after reload"): the verifications of a repository location and
// the restores of a backup's subject, matched in the running list so a
// reload or coming back finds them again. Pure; tested in jobs.spec.ts.
import type { Job } from '$lib/api/client';
import type { JobMatch, TargetMatch } from '$lib/features/jobs/active';
import type { Backup } from './model';

/** Verification job kinds: an environment's location, the manager's. */
export const VERIFY_KINDS = ['backup.verify', 'manager.verify'] as const;

/**
 * The verifications of a repository: of one location (`scope`, e.g.
 * `env:<id>` or `manager`, the location that holds a backup) or of all of
 * them. A verification targets its repository; an environment location's
 * runs in that environment.
 */
export function verifyMatch(repositoryId: string, scope?: string): JobMatch {
	const targets = [{ type: 'repository', id: repositoryId }];
	if (scope === 'manager') return { kinds: ['manager.verify'], targets };
	const env = scope?.startsWith('env:') ? scope.slice(4) : '';
	if (env) return { kinds: ['backup.verify'], targets, environmentId: env };
	return { kinds: [...VERIFY_KINDS], targets };
}

/** "Verify NAS for Silo" / "Verify NAS (Manager State)". */
export function verifyTitle(
	job: Pick<Job, 'kind' | 'environmentId'>,
	repository: string,
	environmentName: (id: string) => string
): string {
	if (job.kind === 'manager.verify' || !job.environmentId)
		return `Verify ${repository} (Manager State)`;
	return `Verify ${repository} for ${environmentName(job.environmentId)}`;
}

/**
 * The restores of what a backup holds, now (null: a manager state backup,
 * restored on a new Docker Manager): its stack (definition, files, or a
 * file of it) and its volumes, or only `volume` (a volume's page). A
 * restore targets the stack and each volume it writes.
 */
export function restoreMatch(
	b: Pick<Backup, 'kind' | 'stackId' | 'environmentId' | 'volume' | 'volumes'>,
	volume?: string
): JobMatch | null {
	const vol = (id: string): TargetMatch => ({
		type: 'volume',
		id,
		...(b.environmentId ? { environmentId: b.environmentId } : {})
	});
	let targets: TargetMatch[] = [];
	if (volume) targets = [vol(volume)];
	else if (b.kind === 'stack')
		targets = [
			...(b.stackId ? [{ type: 'stack', id: b.stackId }] : []),
			...(b.volumes ?? []).map(vol)
		];
	else if (b.kind === 'volume' && b.volume) targets = [vol(b.volume)];
	return targets.length ? { kinds: ['restore.run'], targets } : null;
}
