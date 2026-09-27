// Restores from a stack's or volume's Backups tab (#10): what is sent and
// what the confirmation says. Full restores replace everything the backup
// holds for the subject; paths restores replace only the selected items.
import type { Job } from '$lib/api/client';
import type { Backup } from './model';

export type RestorePlan = { kind: 'full' } | { kind: 'paths'; paths: string[] };

export interface RestoreBody {
	scope: 'full' | 'paths' | 'volume';
	paths?: string[];
	volumes?: string[];
	redeploy?: boolean;
}

/**
 * The request body: a volume backup (or one volume of a stack backup, on
 * a volume's page) restores as a volume, which every agent serves; a
 * stack backup restores whole (optionally redeploying) or by paths.
 */
export function restoreBody(
	b: Pick<Backup, 'kind'>,
	plan: RestorePlan,
	o: { volume?: string; redeploy?: boolean } = {}
): RestoreBody {
	if (plan.kind === 'paths') return { scope: 'paths', paths: plan.paths };
	if (b.kind === 'volume') return { scope: 'volume' };
	if (o.volume) return { scope: 'volume', volumes: [o.volume] };
	return o.redeploy ? { scope: 'full', redeploy: true } : { scope: 'full' };
}

/** Plain-language consequences shown before a restore is confirmed. */
export function restoreConsequences(
	b: Pick<Backup, 'kind' | 'volumes' | 'volume'>,
	plan: RestorePlan,
	o: { subject: string; volume?: string; redeploy?: boolean; running: number }
): string[] {
	const out: string[] = [];
	if (plan.kind === 'paths') {
		const n = plan.paths.length;
		out.push(
			`Only the ${n === 1 ? 'selected item is' : `${n} selected items are`} replaced by the backup's version. Everything else stays as it is.`,
			'A selected folder is made identical to the backup: files added to it since are removed.'
		);
	} else if (b.kind === 'stack' && !o.volume) {
		const vols = b.volumes ?? [];
		out.push(
			`Everything in ${o.subject} is replaced by the backup: the project directory (compose.yaml, .env and files)${
				vols.length
					? ` and ${vols.length === 1 ? 'the volume' : `${vols.length} volumes`} ${vols.join(', ')}`
					: ''
			}.`,
			'Files created since the backup are removed.'
		);
	} else {
		out.push(
			`Everything in volume ${o.volume ?? b.volume ?? o.subject} is replaced by the backup; files created since are removed.`
		);
	}
	out.push(
		o.running
			? `${o.running} running ${o.running === 1 ? 'container stops' : 'containers stop'} first and ${o.running === 1 ? 'starts' : 'start'} again afterwards; stopped ones stay stopped.`
			: 'No running container uses this data.',
		'Starting, restarting or deploying the affected containers is refused until the restore finished.'
	);
	if (o.redeploy) out.push(`Afterwards ${o.subject} is deployed from the restored definition.`);
	out.push('If anything fails, the original files are moved back.');
	return out;
}

/** A restore that has not ended (it holds back starts of its data). */
export function activeRestore(jobs: readonly Job[] | undefined): Job | undefined {
	return jobs?.find(
		(j) =>
			j.kind === 'restore.run' &&
			['queued', 'blocked', 'dispatched', 'running', 'cancelling'].includes(j.state)
	);
}
