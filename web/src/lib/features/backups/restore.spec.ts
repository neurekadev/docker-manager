import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { activeRestore, restoreBody, restoreConsequences } from './restore';

describe('restores from a Backups tab (#10)', () => {
	it('sends the scope the subject needs', () => {
		const stack = { kind: 'stack' as const };
		expect(restoreBody(stack, { kind: 'full' })).toEqual({ scope: 'full' });
		expect(restoreBody(stack, { kind: 'full' }, { redeploy: true })).toEqual({
			scope: 'full',
			redeploy: true
		});
		expect(restoreBody(stack, { kind: 'full' }, { volume: 'shop_db', redeploy: true })).toEqual(
			{
				scope: 'volume',
				volumes: ['shop_db']
			}
		);
		expect(restoreBody({ kind: 'volume' }, { kind: 'full' })).toEqual({ scope: 'volume' });
		expect(restoreBody(stack, { kind: 'paths', paths: ['/a', '/b'] })).toEqual({
			scope: 'paths',
			paths: ['/a', '/b']
		});
	});

	it('says what gets replaced', () => {
		const full = restoreConsequences(
			{ kind: 'stack', volumes: ['shop_db'] },
			{ kind: 'full' },
			{ subject: 'shop', redeploy: true, running: 2 }
		);
		expect(full[0]).toBe(
			'Everything in shop is replaced by the backup: the project directory (compose.yaml, .env and files) and the volume shop_db.'
		);
		expect(full).toContain(
			'2 running containers stop first and start again afterwards; stopped ones stay stopped.'
		);
		expect(full).toContain('Afterwards shop is deployed from the restored definition.');
		const paths = restoreConsequences(
			{ kind: 'stack' },
			{ kind: 'paths', paths: ['/a'] },
			{ subject: 'shop', running: 0 }
		);
		expect(paths[0]).toBe(
			"Only the selected item is replaced by the backup's version. Everything else stays as it is."
		);
		expect(paths).toContain('No running container uses this data.');
		expect(
			restoreConsequences(
				{ kind: 'volume', volume: 'up' },
				{ kind: 'full' },
				{ subject: 'up', running: 1 }
			)[0]
		).toBe(
			'Everything in volume up is replaced by the backup; files created since are removed.'
		);
	});

	it('finds a restore that has not ended', () => {
		const j = (kind: string, state: string) =>
			({ id: kind + state, kind, state }) as unknown as Job;
		expect(
			activeRestore([j('stack.deploy', 'running'), j('restore.run', 'succeeded')])
		).toBeUndefined();
		expect(activeRestore([j('restore.run', 'queued')])?.id).toBe('restore.runqueued');
	});
});
