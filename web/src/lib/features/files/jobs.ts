// The file jobs of one root (#15, docs/internal/web.md "Job progress after
// reload"): which running jobs the file manager shows again after a reload
// or when the user comes back, and what it calls them. Stack and volume
// roots run `files.*` jobs in their environment with the root as a target
// (`stack:<stackId>` or `volume:<name>`, beside `path:/<kind>/<id>/<dir>`
// locks); template drafts run `template.files.*` jobs on `template:<id>`,
// outside any environment. Pure; tested in jobs.spec.ts.
import type { Job } from '$lib/api/client';
import type { JobMatch } from '$lib/features/jobs/active';
import { jobKindLabel } from '$lib/features/jobs/labels';
import type { FileScope } from './api';

/** The running jobs of the file manager of `scope`. */
export function fileJobMatch(scope: FileScope): JobMatch {
	switch (scope.kind) {
		case 'stack':
			return {
				kindPrefix: 'files.',
				environmentId: scope.environmentId,
				targets: [{ type: 'stack', id: scope.stackId }]
			};
		case 'volume':
			return {
				kindPrefix: 'files.',
				environmentId: scope.environmentId,
				targets: [{ type: 'volume', id: scope.volume, environmentId: scope.environmentId }]
			};
		case 'template':
			return {
				kindPrefix: 'template.files.',
				targets: [{ type: 'template', id: scope.templateId }]
			};
	}
}

/** The root-relative folder a file job acts in ("config"), "" for the root. */
export function fileJobDir(job: Pick<Job, 'targets'>, scope: FileScope): string {
	if (scope.kind === 'template') return '';
	const root = `/${scope.kind}/${scope.kind === 'stack' ? scope.stackId : scope.volume}`;
	const p = job.targets?.find((t) => t.type === 'path')?.id;
	if (!p || !p.startsWith(`${root}/`)) return '';
	return p.slice(root.length + 1);
}

/**
 * The title of a file job the view did not start itself (found again in
 * the running list): "Copy files in config", "Delete files in silo".
 */
export function fileJobTitle(
	job: Pick<Job, 'kind' | 'targets'>,
	scope: FileScope,
	rootLabel: string
): string {
	return `${jobKindLabel(job.kind)} in ${fileJobDir(job, scope) || rootLabel}`;
}
