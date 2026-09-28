// Backup actions (#10) shared by the overview rows, the drawers and the
// policy page: run a policy now, retry the members a set missed. Toasts
// name the result; mutations invalidate by prefix and never retry.
import type { QueryClient } from '@tanstack/svelte-query';
import { goto } from '$app/navigation';
import { api, unwrap } from '$lib/api/client';
import { routes } from '$lib/routes';
import { toast } from '$lib/ui';
import { newIdempotencyKey } from '$lib/features/common/data';
import { actionError } from '$lib/features/common/errors';
import { incompleteMembers, type BackupSet } from './model';

function refresh(qc: QueryClient) {
	void qc.invalidateQueries({ queryKey: ['policies'] });
	void qc.invalidateQueries({ queryKey: ['backups'] });
	void qc.invalidateQueries({ queryKey: ['jobs'] });
}

/** Starts a backup of the policy now; false when it did not start. */
export async function runPolicy(
	qc: QueryClient,
	p: { id: string; name: string }
): Promise<boolean> {
	try {
		const out = await unwrap(
			api.POST('/api/v1/backup-policies/{policyId}/runs', {
				params: {
					path: { policyId: p.id },
					header: { 'Idempotency-Key': newIdempotencyKey() }
				},
				body: {}
			})
		);
		refresh(qc);
		toast.info(`Started a backup of ${p.name}`, {
			body: `${out.jobs.length} ${out.jobs.length === 1 ? 'job' : 'jobs'}; progress shows under Running now.`,
			action: { label: 'Open policy', onclick: () => void goto(routes.backupPolicy(p.id)) }
		});
		return true;
	} catch (e) {
		toast.error(`${p.name} was not backed up`, {
			body: actionError(e, {
				recovery_key_not_confirmed:
					'Confirm the Recovery Key of the repositories this policy uses first.',
				backup_run_active:
					'A backup of this policy is already running. Wait for it to finish.'
			})
		});
		return false;
	}
}

/** Runs again only the members of a set that did not complete. */
export async function retrySet(
	qc: QueryClient,
	s: BackupSet & { policyId?: string }
): Promise<boolean> {
	if (!s.policyId) return false;
	try {
		await unwrap(
			api.POST('/api/v1/backup-policies/{policyId}/runs', {
				params: {
					path: { policyId: s.policyId },
					header: { 'Idempotency-Key': newIdempotencyKey() }
				},
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
