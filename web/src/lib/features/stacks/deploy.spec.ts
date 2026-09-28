import { describe, expect, it } from 'vitest';
import {
	deployFailure,
	deploySuccess,
	deployTitle,
	driftNotes,
	lastUpdateCheck,
	orphanedServices,
	pendingUpdates,
	pullResult,
	updateAvailable,
	updateSuccess
} from './model';

describe('deploy outcomes', () => {
	it('names what runs and what failed', () => {
		expect(deployTitle('Silo', {})).toBe('Deploy Silo');
		expect(deployTitle('Silo', { build: true })).toBe('Build and deploy Silo');
		expect(deployTitle('Silo', { removeOrphans: true })).toBe('Deploy Silo and remove orphans');
		expect(deployFailure('Silo', {})).toBe('Silo was not deployed');
	});

	it('says when a deploy changed nothing (the last deploy time stayed)', () => {
		const t1 = '2026-09-27T10:00:00Z';
		const t2 = '2026-09-27T11:00:00Z';
		expect(deploySuccess('Silo', {}, t1, t2)).toBe('Deployed Silo');
		expect(deploySuccess('Silo', {}, t1, t1)).toBe(
			'Nothing to deploy: Silo already runs its definition'
		);
		// Never deployed by Docker Manager, and still nothing started.
		expect(deploySuccess('Silo', {}, undefined, undefined)).toBe(
			'Nothing to deploy: Silo already runs its definition'
		);
		expect(deploySuccess('Silo', { build: true }, t1, t2)).toBe('Built and deployed Silo');
		expect(deploySuccess('Silo', { removeOrphans: true }, t1, t1)).toBe(
			'Removed the orphaned containers of Silo; everything else already ran its definition'
		);
		expect(deploySuccess('Silo', { removeOrphans: true }, t1, t2)).toBe(
			'Deployed Silo and removed its orphaned containers'
		);
	});
});

describe('pull outcomes', () => {
	it('names the services with a newer image, or says the images are up to date', () => {
		expect(
			pullResult(
				'Silo',
				[{ service: 'web' }, { service: 'db' }],
				[
					{ service: 'web', pulledImageId: 'sha256:w2' },
					{ service: 'db', pulledImageId: 'sha256:d2' }
				]
			)
		).toEqual({
			title: 'Pulled newer images for web, db: deploy to run them',
			newer: ['web', 'db']
		});
		expect(pullResult('Silo', [{ service: 'web' }], [{ service: 'web' }])).toEqual({
			title: 'Images of Silo are up to date',
			newer: []
		});
		// Pulled before, not deployed since: nothing new this time.
		expect(
			pullResult(
				'Silo',
				[{ service: 'web', pulledImageId: 'sha256:w2' }],
				[{ service: 'web', pulledImageId: 'sha256:w2' }]
			)
		).toMatchObject({
			title: 'No newer images since the last pull',
			body: 'web waits for a deploy to run the image pulled before.',
			newer: []
		});
		// Without image status (no stack.read): a plain confirmation.
		expect(pullResult('Silo', undefined, undefined).title).toBe('Pulled the images of Silo');
	});
});

describe('update outcomes', () => {
	const img = (over: Record<string, unknown>) =>
		({
			service: 'web',
			image: 'nginx:1.27',
			build: false,
			eligible: true,
			nonVersionTag: false,
			update: 'up_to_date',
			...over
		}) as never;

	it('lists the services with a newer image by image and tag, never a digest', () => {
		const images = [
			img({ service: 'web', update: 'update_available', candidateDigest: 'sha256:abc' }),
			img({ service: 'db', image: 'postgres:16', pulledImageId: 'sha256:d2' }),
			img({ service: 'cache', image: 'redis:7' }),
			img({ service: 'app', image: 'app', build: true, update: 'update_available' })
		];
		expect(pendingUpdates(images)).toEqual([
			{ service: 'db', image: 'postgres:16', pulled: true },
			{ service: 'web', image: 'nginx:1.27', pulled: false }
		]);
		expect(updateAvailable(images)).toBe(true);
		expect(pendingUpdates([img({})])).toEqual([]);
		expect(updateAvailable([img({})])).toBe(false);
		expect(pendingUpdates(undefined)).toEqual([]);
	});

	it('finds the newest update check', () => {
		expect(
			lastUpdateCheck([
				img({ checkedAt: '2026-09-27T10:00:00Z' }),
				img({ checkedAt: '2026-09-27T12:00:00Z' }),
				img({})
			])
		).toBe('2026-09-27T12:00:00Z');
		expect(lastUpdateCheck([img({})])).toBeUndefined();
	});

	it('says when an update recreated nothing', () => {
		const t1 = '2026-09-27T10:00:00Z';
		expect(updateSuccess('Silo', t1, '2026-09-27T11:00:00Z')).toBe('Updated Silo');
		expect(updateSuccess('Silo', t1, t1)).toBe(
			'Nothing to update: Silo already runs the newest images'
		);
	});
});

describe('drift', () => {
	const services = [
		{ name: 'web', drift: ['not_running'] },
		{ name: 'forgejo-runner-register', drift: ['unexpected_service'] },
		{ name: 'db', drift: [] }
	];

	it('finds orphans', () => {
		expect(orphanedServices(services)).toEqual(['forgejo-runner-register']);
		expect(orphanedServices(undefined)).toEqual([]);
	});

	it('words each finding with its remedy, orphans first', () => {
		expect(driftNotes(services)).toEqual([
			{
				service: 'forgejo-runner-register',
				orphan: true,
				text: 'forgejo-runner-register is no longer in the Compose file, but its container is still on the host. “Remove old containers” deploys the stack and removes it.'
			},
			{
				service: 'web',
				orphan: false,
				text: 'web is not running. Start it, or deploy the stack.'
			}
		]);
		expect(driftNotes([{ name: 'x', drift: ['future_kind'] }])[0].text).toBe('x: future kind.');
	});
});
