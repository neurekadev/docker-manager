// Backup actions (#10, #246) shared by the Backups page, the drawers and
// the stack pages: back up now, retry the members a set missed. Toasts
// name the result; mutations invalidate by prefix and never retry.
import type { QueryClient } from '@tanstack/svelte-query';
import { api, unwrap, type Job } from '$lib/api/client';
import { toast } from '$lib/ui';
import { newIdempotencyKey } from '$lib/features/common/data';
import { actionError } from '$lib/features/common/errors';
import { incompleteMembers, type BackupSet } from './model';

function refresh(qc: QueryClient) {
	void qc.invalidateQueries({ queryKey: ['policies'] });
	void qc.invalidateQueries({ queryKey: ['backups'] });
	void qc.invalidateQueries({ queryKey: ['jobs'] });
}

/** The errors of a backup that did not start, in words. */
export const BACK_UP_ERRORS = {
	recovery_key_not_confirmed:
		'Confirm the Recovery Key of the Primary and Secondary repositories first.',
	backup_run_active: 'A backup is already running. Wait for it to finish.',
	backup_no_primary: 'Make a repository the Primary one first.'
};

/** Starts a backup now; the started jobs, or null when it did not start. */
export async function backUpNow(qc: QueryClient): Promise<Job[] | null> {
	try {
		const out = await unwrap(
			api.POST('/api/v1/backup-settings/runs', {
				params: { header: { 'Idempotency-Key': newIdempotencyKey() } },
				body: {}
			})
		);
		refresh(qc);
		toast.info('Started a backup', {
			body: `${out.jobs.length} ${out.jobs.length === 1 ? 'job' : 'jobs'}; progress shows under Running Now.`
		});
		return out.jobs;
	} catch (e) {
		toast.error('Nothing was backed up', { body: actionError(e, BACK_UP_ERRORS) });
		return null;
	}
}

/** Runs again only the members of a set that did not complete. */
export async function retrySet(qc: QueryClient, s: BackupSet): Promise<boolean> {
	try {
		await unwrap(
			api.POST('/api/v1/backup-settings/runs', {
				params: { header: { 'Idempotency-Key': newIdempotencyKey() } },
				body: { retrySetId: s.id }
			})
		);
		refresh(qc);
		toast.success(`Retrying ${incompleteMembers(s).length} of ${s.members.length} backups`, {
			body: 'Only the backups that did not complete run again; the set keeps its ID.'
		});
		return true;
	} catch (e) {
		toast.error('The retry did not start', {
			body: actionError(e, {
				nothing_to_retry: 'Every backup of this set already completed.'
			})
		});
		return false;
	}
}
