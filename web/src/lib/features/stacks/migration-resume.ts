// A stack migration the wizard finds running (docs/internal/web.md, "Job
// progress after reload"): after a reload, or when the user comes back to
// the migrate page, the wizard opens at the move step on the stack's
// running stack.migrate job instead of starting over. The job runs in the
// source environment; its copied volumes are targets there, and their
// copies on the destination are targets in the destination. Pure; tested
// in migration-resume.spec.ts.
import type { Job } from '$lib/api/client';

/** The stack's migrations the wizard follows. */
export function migrationMatch(stackId: string) {
	return { targets: [{ type: 'stack', id: stackId }], kinds: ['stack.migrate'] };
}

export interface ResumedMigration {
	/** The environment the stack moves away from. */
	source: string;
	/** Where it goes; empty when the job does not say (no volume is copied). */
	target: string;
	/** The source volumes it copies. */
	volumes: string[];
}

/**
 * Source, destination and copied volumes of a running migration. Without
 * a copied volume the destination is where the stack already lives (after
 * the cut-over), or the only other environment.
 */
export function resumedMigration(
	job: Pick<Job, 'environmentId' | 'targets'>,
	stack: { environmentId: string },
	envs: readonly { id: string }[] | undefined
): ResumedMigration {
	const source = job.environmentId || stack.environmentId;
	const volumes = (job.targets ?? []).filter((t) => t.type === 'volume');
	const moved = volumes.find((t) => t.environmentId && t.environmentId !== source);
	const others = (envs ?? []).filter((e) => e.id !== source);
	const target =
		moved?.environmentId ??
		(stack.environmentId !== source
			? stack.environmentId
			: others.length === 1
				? others[0].id
				: '');
	return {
		source,
		target,
		volumes: volumes
			.filter((t) => !t.environmentId || t.environmentId === source)
			.map((t) => t.id)
	};
}
