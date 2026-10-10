// Stack archives (#313): the export check and the export job of a stack,
// the archive's download, and creating a stack from an uploaded archive
// (its check, the import job and discarding the upload; the upload itself
// is archive-upload.ts). Calls that start a job carry a fresh
// Idempotency-Key; nothing retries on its own.
import { api, unwrap, unwrapEmpty, type ApiClient, type Job, type Schema } from '$lib/api/client';
import { toast } from '$lib/ui';
import { stackJobCopy } from './adopt';
import { exportDownloadUrl } from './archives';
import type { TrackedJob } from './tray.svelte';

const key = () => crypto.randomUUID();

/** POST /stacks/{id}/export-previews: what an export does. Changes nothing. */
export function previewStackExport(
	stackId: string,
	body: Schema<'StackExportBody'>,
	client: ApiClient = api
): Promise<Schema<'StackExportPreview'>> {
	return unwrap(
		client.POST('/api/v1/stacks/{stackId}/export-previews', {
			params: { path: { stackId } },
			body
		})
	);
}

/** POST /stacks/{id}/exports: a stack.export job writes the archive (its ID is the export's). */
export function startStackExport(
	stackId: string,
	body: Schema<'StackExportBody'>,
	client: ApiClient = api
): Promise<Job> {
	return unwrap(
		client.POST('/api/v1/stacks/{stackId}/exports', {
			params: { path: { stackId }, header: { 'Idempotency-Key': key() } },
			body
		})
	);
}

/**
 * Downloads an export's archive: a link click, so the browser streams the
 * file to disk (Content-Disposition names it) instead of holding it in
 * memory.
 */
export function downloadStackExport(stackId: string, exportId: string) {
	const a = document.createElement('a');
	a.href = exportDownloadUrl(stackId, exportId);
	a.download = '';
	document.body.appendChild(a);
	a.click();
	a.remove();
}

/**
 * The stack page's job tray entry of an export the export dialog does not
 * show (closed while it ran, or found running after a reload): its
 * success toast offers the download.
 */
export function exportTrayEntry(stackId: string, title: string): Omit<TrackedJob, 'id'> {
	const copy = stackJobCopy('stack.export', title);
	return {
		kind: 'stack.export',
		...copy,
		successFor: async (job) => ({
			title: copy.success,
			action: {
				label: 'Download Archive',
				onclick: () => {
					downloadStackExport(stackId, job.id);
					toast.info(`Downloading the archive of ${title}`);
				}
			}
		})
	};
}

/** DELETE /stack-archives/{id}: discards the caller's upload. */
export function discardStackArchive(archiveId: string, client: ApiClient = api): Promise<void> {
	return unwrapEmpty(
		client.DELETE('/api/v1/stack-archives/{archiveId}', {
			params: { path: { archiveId } }
		})
	);
}

/** POST /stack-archives/{id}/import-previews: checks the environment. Changes nothing. */
export function previewArchiveImport(
	archiveId: string,
	body: Schema<'StackArchiveImportBody'>,
	client: ApiClient = api
): Promise<Schema<'StackArchiveImportPreview'>> {
	return unwrap(
		client.POST('/api/v1/stack-archives/{archiveId}/import-previews', {
			params: { path: { archiveId } },
			body
		})
	);
}

/**
 * POST /stack-archives/{id}/imports: a stack.import_archive job fills the
 * new stack (its stack target, created at once) from the archive.
 */
export function startArchiveImport(
	archiveId: string,
	body: Schema<'StackArchiveImportBody'>,
	client: ApiClient = api
): Promise<Job> {
	return unwrap(
		client.POST('/api/v1/stack-archives/{archiveId}/imports', {
			params: { path: { archiveId }, header: { 'Idempotency-Key': key() } },
			body
		})
	);
}
