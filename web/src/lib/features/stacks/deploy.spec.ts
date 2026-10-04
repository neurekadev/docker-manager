import { describe, expect, it } from 'vitest';
import {
	deployFailure,
	deploySuccess,
	deployTitle,
	driftNotes,
	orphanedServices,
	pendingUpdates,
	updateAvailable
} from './model';

describe('deploy outcomes', () => {
	it('names what runs and what failed', () => {
		expect(deployTitle('Silo', {})).toBe('Deploy Silo');
		expect(deployTitle('Silo', { build: true })).toBe('Build and Deploy Silo');
		expect(deployTitle('Silo', { removeOrphans: true })).toBe('Deploy Silo and Remove Orphans');
		expect(deployTitle('Silo', { pull: true })).toBe('Pull and Deploy Silo');
		expect(deployFailure('Silo', {})).toBe('Silo was not deployed');
		expect(deployFailure('Silo', { pull: true })).toBe('Silo was not pulled and deployed');
		expect(deployTitle('Silo', { pull: true, build: true })).toBe(
			'Pull, Build and Deploy Silo'
		);
		expect(deployFailure('Silo', { pull: true, build: true })).toBe(
			'Silo was not pulled, built and deployed'
		);
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
		expect(deploySuccess('Silo', { pull: true, build: true }, t1, t2)).toBe(
			'Pulled newer images, built and deployed Silo'
		);
		// A pull and rebuild that changed nothing still built the images.
		expect(deploySuccess('Silo', { pull: true, build: true }, t1, t1)).toBe(
			'Built the images of Silo; nothing needed to be redeployed'
		);
		expect(deploySuccess('Silo', { pull: true }, t1, t1)).toBe(
			'Nothing to update: Silo already runs the newest images'
		);
		expect(deploySuccess('Silo', { removeOrphans: true }, t1, t1)).toBe(
			'Removed the orphaned containers of Silo; everything else already ran its definition'
		);
		expect(deploySuccess('Silo', { removeOrphans: true }, t1, t2)).toBe(
			'Deployed Silo and removed its orphaned containers'
		);
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
				text: 'forgejo-runner-register is no longer in the Compose file, but its container is still on the host. “Remove Old Containers” deploys the stack and removes it.'
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
