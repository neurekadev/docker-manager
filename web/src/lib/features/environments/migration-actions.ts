// Environment migration requests (#35): the check, the start (a fresh
// Idempotency-Key each, so a retried click never runs twice) and the
// removal of the old copies a run left stopped on the source: one
// source-removal request per moved stack. The wizard starts them
// (startOldCopyRemovals) and follows the jobs as tracked jobs, so they
// survive a reload; removeOldCopies also follows each job until it ends,
// then resolves with one outcome for the summary toast (bulkSummary).
// Nothing retries on its own.
import { api, unwrap, type ApiClient, type Job } from '$lib/api/client';
import { JobWatcher } from '$lib/api/jobs.svelte';
import type { BulkOutcome } from '$lib/features/resources/bulk';
import { removeMigrationSource } from '$lib/features/stacks/actions';
import type {
	EnvironmentMigrationBody,
	EnvironmentMigrationPreview,
	OldCopy
} from './environment-migration';

const key = () => crypto.randomUUID();

/** POST /environments/{id}/migration-previews: the check (changes nothing). */
export function previewEnvironmentMigration(
	environmentId: string,
	body: EnvironmentMigrationBody,
	client: ApiClient = api
): Promise<EnvironmentMigrationPreview> {
	return unwrap(
		client.POST('/api/v1/environments/{environmentId}/migration-previews', {
			params: { path: { environmentId } },
			body
		})
	);
}

/** POST /environments/{id}/migrations: starts the environment.migrate job. */
export function startEnvironmentMigration(
	environmentId: string,
	body: EnvironmentMigrationBody,
	client: ApiClient = api
): Promise<Job> {
	return unwrap(
		client.POST('/api/v1/environments/{environmentId}/migrations', {
			params: { path: { environmentId }, header: { 'Idempotency-Key': key() } },
			body
		})
	);
}

const sendRemoval = (c: OldCopy) => removeMigrationSource(c.stackId, c.migrationId);

/** The removals started: each copy's job, and the copies whose request was refused. */
export interface StartedRemovals<C extends OldCopy = OldCopy> {
	started: { copy: C; job: Job }[];
	refused: C[];
}

/**
 * Starts the removal of each old copy (one request per copy, all at once)
 * and resolves once every request was answered; the caller follows the
 * jobs (useTrackedJobs).
 */
export async function startOldCopyRemovals<C extends OldCopy>(
	copies: readonly C[],
	send: (copy: C) => Promise<Job> = sendRemoval
): Promise<StartedRemovals<C>> {
	const answers = await Promise.allSettled(copies.map((c) => send(c)));
	const out: StartedRemovals<C> = { started: [], refused: [] };
	answers.forEach((a, i) => {
		if (a.status === 'fulfilled') out.started.push({ copy: copies[i], job: a.value });
		else out.refused.push(copies[i]);
	});
	return out;
}

export interface RemoveOldCopiesOptions {
	/** Sends one removal (default: the stack's source-removal request). */
	send?: (copy: OldCopy) => Promise<Pick<Job, 'id'>>;
	/** Follows a job until it ends (default: a JobWatcher). */
	watch?: (jobId: string, onfinish: (job: Job) => void) => void;
}

/**
 * Removes the stopped copies of the moved stacks from the source: one
 * request per copy, every accepted job followed until it ends. Resolves
 * with the outcome once every job ended; refused requests count as failed.
 */
export function removeOldCopies(
	copies: readonly OldCopy[],
	o: RemoveOldCopiesOptions = {}
): Promise<BulkOutcome> {
	const send = o.send ?? sendRemoval;
	const watch =
		o.watch ??
		((id: string, onfinish: (job: Job) => void) => {
			new JobWatcher(id, { onfinish }).start();
		});
	const outcome: BulkOutcome = { succeeded: [], failed: [], refused: [], skipped: 0 };
	return new Promise((resolve) => {
		let pending = copies.length;
		const done = () => {
			if (--pending === 0) resolve(outcome);
		};
		if (pending === 0) resolve(outcome);
		for (const c of copies)
			send(c).then(
				(job) =>
					watch(job.id, (j) => {
						(j.state === 'succeeded' ? outcome.succeeded : outcome.failed).push(
							c.title
						);
						done();
					}),
				() => {
					outcome.failed.push(c.title);
					done();
				}
			);
	});
}
