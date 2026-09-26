// Stack mutations (#22 track B2, #7/#20/#33/#35). Every call that starts a
// job carries a fresh Idempotency-Key (a retried click never runs twice);
// edits carry If-Match with the stack's revision. Callers invalidate the
// affected queries (stackKeys) after success; nothing retries on its own.
import { api, unwrap, type ApiClient, type Job, type Schema } from '$lib/api/client';
import type { Stack } from './queries';

export type DeployMode = 'deploy' | 'pull' | 'build';
export type StackOperation = 'start' | 'stop' | 'restart' | 'down';

const key = () => crypto.randomUUID();

/** The strong ETag of a revision counter (api.RevisionETag). */
export function etag(revision: number | undefined): string {
	return `"${revision ?? 0}"`;
}

/** POST /stacks/{id}/deployments: plain, with fresh images, or rebuilt. */
export function deployStack(
	stackId: string,
	mode: DeployMode,
	client: ApiClient = api
): Promise<Job> {
	const body: Schema<'DeployStackInputBody'> = {};
	if (mode === 'pull') body.pull = 'always';
	if (mode === 'build') body.build = true;
	return unwrap(
		client.POST('/api/v1/stacks/{stackId}/deployments', {
			params: { path: { stackId }, header: { 'Idempotency-Key': key() } },
			body
		})
	);
}

/** POST /stacks/{id}/operations: start, stop, restart or down. */
export function operateStack(
	stackId: string,
	action: StackOperation,
	services?: string[],
	client: ApiClient = api
): Promise<Job> {
	return unwrap(
		client.POST('/api/v1/stacks/{stackId}/operations', {
			params: { path: { stackId }, header: { 'Idempotency-Key': key() } },
			body: { action, services: services?.length ? services : undefined }
		})
	);
}

/** DELETE /stacks/{id}: takes it down, keeps volumes and the project directory. */
export function deleteStack(stackId: string, client: ApiClient = api): Promise<Job> {
	return unwrap(
		client.DELETE('/api/v1/stacks/{stackId}', {
			params: { path: { stackId }, header: { 'Idempotency-Key': key() } }
		})
	);
}

/** PATCH /stacks/{id}: display metadata only (never Compose files). */
export function patchStack(
	stack: Pick<Stack, 'id' | 'revision'>,
	patch: Schema<'UpdateStackInputBody'>,
	client: ApiClient = api
): Promise<Stack> {
	return unwrap(
		client.PATCH('/api/v1/stacks/{stackId}', {
			params: { path: { stackId: stack.id }, header: { 'If-Match': etag(stack.revision) } },
			body: patch
		})
	);
}

/** POST /stacks/{id}/revision-restores: writes a revision back to disk. */
export function restoreRevision(
	stackId: string,
	revisionId: string,
	client: ApiClient = api
): Promise<Schema<'StackRestoreResult'>> {
	return unwrap(
		client.POST('/api/v1/stacks/{stackId}/revision-restores', {
			params: { path: { stackId } },
			body: { revisionId }
		})
	);
}

export function validateStack(
	body: Schema<'ValidateStackInputBody'>,
	client: ApiClient = api
): Promise<Schema<'StackValidation'>> {
	return unwrap(client.POST('/api/v1/stacks/validations', { body }));
}

export function createStack(
	body: Schema<'CreateStackInputBody'>,
	client: ApiClient = api
): Promise<Schema<'CreateStackOutputBody'>> {
	return unwrap(client.POST('/api/v1/stacks', { body }));
}

export function importStack(
	environmentId: string,
	body: Schema<'ImportStackInputBody'>,
	client: ApiClient = api
): Promise<Stack> {
	return unwrap(
		client.POST('/api/v1/environments/{environmentId}/stacks/imports', {
			params: { path: { environmentId } },
			body
		})
	);
}

export function previewMigration(
	stackId: string,
	body: Schema<'StackMigrationBody'>,
	client: ApiClient = api
): Promise<Schema<'MigrationPreview'>> {
	return unwrap(
		client.POST('/api/v1/stacks/{stackId}/migration-previews', {
			params: { path: { stackId } },
			body
		})
	);
}

export function startMigration(
	stackId: string,
	body: Schema<'StackMigrationBody'>,
	client: ApiClient = api
): Promise<Job> {
	return unwrap(
		client.POST('/api/v1/stacks/{stackId}/migrations', {
			params: { path: { stackId }, header: { 'Idempotency-Key': key() } },
			body
		})
	);
}

export function removeMigrationSource(
	stackId: string,
	migrationId: string,
	client: ApiClient = api
): Promise<Job> {
	return unwrap(
		client.POST('/api/v1/stacks/{stackId}/migrations/{migrationId}/source-removals', {
			params: { path: { stackId, migrationId }, header: { 'Idempotency-Key': key() } }
		})
	);
}

export interface SourceRestart {
	started: string[];
	failed: { container: string; message: string }[];
}

const TERMINAL = ['succeeded', 'failed', 'partial', 'cancelled', 'interrupted'];

/**
 * Starts the stopped containers a completed migration left on the source
 * (#35: the source stays stopped until the user removes it or starts it
 * again), one container.start job at a time in dependency order, waiting
 * for each job to end. The stack itself stays on the destination.
 */
export async function restartSource(
	environmentId: string,
	projectName: string,
	order: string[],
	client: ApiClient = api,
	wait: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms)),
	maxPolls = 120
): Promise<SourceRestart> {
	const page = await unwrap(
		client.GET('/api/v1/environments/{environmentId}/containers', {
			params: { path: { environmentId }, query: { stack: projectName, limit: 200 } }
		})
	);
	const rank = (c: Schema<'Container'>) => {
		const i = order.indexOf(c.stack?.service ?? '');
		return i < 0 ? order.length : i;
	};
	const stopped = page.items
		.filter((c) => c.state !== 'running' && c.actions.includes('container.start'))
		.sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
	const out: SourceRestart = { started: [], failed: [] };
	for (const c of stopped) {
		try {
			let job = await unwrap(
				client.POST('/api/v1/environments/{environmentId}/containers/{containerId}/start', {
					params: {
						path: { environmentId, containerId: c.id },
						header: { 'Idempotency-Key': key() }
					}
				})
			);
			for (let i = 0; i < maxPolls && !TERMINAL.includes(job.state); i++) {
				await wait(1000);
				job = await unwrap(
					client.GET('/api/v1/jobs/{jobId}', { params: { path: { jobId: job.id } } })
				);
			}
			if (job.state === 'succeeded') out.started.push(c.name);
			else
				out.failed.push({
					container: c.name,
					message:
						job.error?.message ??
						(TERMINAL.includes(job.state)
							? `The start job ended ${job.state}.`
							: 'The start job is still running; follow it under Jobs.')
				});
		} catch (e) {
			out.failed.push({
				container: c.name,
				message: e instanceof Error ? e.message : String(e)
			});
		}
	}
	return out;
}

// Updates (#20).

export function checkUpdates(policyId: string, client: ApiClient = api): Promise<Job> {
	return unwrap(
		client.POST('/api/v1/update-policies/{policyId}/checks', {
			params: { path: { policyId }, header: { 'Idempotency-Key': key() } }
		})
	);
}

export function previewUpdate(
	policyId: string,
	candidates?: string[],
	client: ApiClient = api
): Promise<Schema<'UpdatePreview'>> {
	return unwrap(
		client.POST('/api/v1/update-policies/{policyId}/previews', {
			params: { path: { policyId } },
			body: { candidates: candidates?.length ? candidates : undefined }
		})
	);
}

export function runUpdate(
	policyId: string,
	previewFingerprint: string,
	candidates?: string[],
	client: ApiClient = api
): Promise<Job> {
	return unwrap(
		client.POST('/api/v1/update-policies/{policyId}/runs', {
			params: { path: { policyId }, header: { 'Idempotency-Key': key() } },
			body: { previewFingerprint, candidates: candidates?.length ? candidates : undefined }
		})
	);
}
