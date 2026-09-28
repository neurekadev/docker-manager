// What the Docker object pages match in the running jobs (object-jobs.ts):
// each page finds its object's jobs again after a reload, and leaves out
// the jobs a tab shows itself.
import { describe, expect, it } from 'vitest';
import { matchJob, type MatchableJob } from '$lib/features/jobs/active';
import {
	containerJobs,
	imageJobs,
	imageReferences,
	kindJobs,
	migrationCopy,
	networkJobs,
	volumeJobs,
	volumeMigrationJobs,
	volumeTab
} from './object-jobs';

function job(
	kind: string,
	targets: { type: string; id: string; environmentId?: string }[],
	environmentId = 'e1'
): MatchableJob {
	return { kind, targets, environmentId } as MatchableJob;
}

describe('containerJobs', () => {
	it("matches the container's jobs in its environment, not update checks", () => {
		const m = containerJobs('e1', 'web');
		expect(matchJob(job('container.restart', [{ type: 'container', id: 'web' }]), m)).toBe(
			true
		);
		expect(matchJob(job('update.run', [{ type: 'container', id: 'web' }]), m)).toBe(true);
		expect(matchJob(job('update.check', [{ type: 'container', id: 'web' }]), m)).toBe(false);
		expect(matchJob(job('container.restart', [{ type: 'container', id: 'db' }]), m)).toBe(
			false
		);
		expect(
			matchJob(job('container.restart', [{ type: 'container', id: 'web' }], 'e2'), m)
		).toBe(false);
	});
});

describe('networkJobs', () => {
	it('matches the network by name', () => {
		const m = networkJobs('e1', 'front');
		expect(matchJob(job('network.remove', [{ type: 'network', id: 'front' }]), m)).toBe(true);
		expect(matchJob(job('network.remove', [{ type: 'network', id: 'back' }]), m)).toBe(false);
	});
});

describe('volumeJobs', () => {
	const files = job('files.delete', [
		{ type: 'volume', id: 'data' },
		{ type: 'path', id: '/volume/data/x' }
	]);
	const migrate = job('volume.migrate', [
		{ type: 'volume', id: 'data' },
		{ type: 'volume', id: 'data', environmentId: 'e2' }
	]);
	const backup = job('backup.run', [{ type: 'volume', id: 'data' }]);

	it('shows every job of the volume on the overview', () => {
		const m = volumeJobs('e1', 'data');
		expect(matchJob(files, m)).toBe(true);
		expect(matchJob(migrate, m)).toBe(true);
		expect(matchJob(backup, m)).toBe(true);
		expect(matchJob(job('volume.remove', [{ type: 'volume', id: 'other' }]), m)).toBe(false);
	});

	it('leaves out the jobs the open tab shows itself', () => {
		expect(matchJob(files, volumeJobs('e1', 'data', 'files'))).toBe(false);
		expect(matchJob(migrate, volumeJobs('e1', 'data', 'files'))).toBe(true);
		// The Backups tab lists no jobs: its backups and restores stay above it.
		expect(matchJob(backup, volumeJobs('e1', 'data', 'backups'))).toBe(true);
		expect(matchJob(migrate, volumeJobs('e1', 'data', 'migrate'))).toBe(false);
	});

	it('reads the tab from the path', () => {
		expect(volumeTab('/volumes/e1/data')).toBeUndefined();
		expect(volumeTab('/volumes/e1/data/files')).toBe('files');
		expect(volumeTab('/volumes/e1/data/backups/')).toBe('backups');
		expect(volumeTab('/volumes/e1/data/migrate')).toBe('migrate');
	});
});

describe('volume migrations', () => {
	it('matches the migrations of the volume and finds the copy', () => {
		const m = volumeMigrationJobs('e1', 'data');
		const run = job('volume.migrate', [
			{ type: 'volume', id: 'data' },
			{ type: 'volume', id: 'data-copy', environmentId: 'e2' }
		]);
		expect(matchJob(run, m)).toBe(true);
		expect(matchJob(job('volume.remove', [{ type: 'volume', id: 'data' }]), m)).toBe(false);
		expect(migrationCopy(run as never)).toEqual({ environmentId: 'e2', name: 'data-copy' });
		expect(migrationCopy(undefined)).toBeUndefined();
	});
});

describe('imageReferences', () => {
	it('lists the references a pull of a Docker Hub tag may have named', () => {
		expect(imageReferences('nginx:latest')).toEqual([
			'nginx:latest',
			'nginx',
			'library/nginx:latest',
			'library/nginx',
			'docker.io/library/nginx:latest',
			'docker.io/library/nginx'
		]);
		expect(imageReferences('acme/app:1.4')).toEqual(['acme/app:1.4', 'docker.io/acme/app:1.4']);
	});

	it('keeps registry, port and digest references as they are', () => {
		expect(imageReferences('ghcr.io/acme/app:1.4')).toEqual(['ghcr.io/acme/app:1.4']);
		expect(imageReferences('localhost:5000/app:2')).toEqual(['localhost:5000/app:2']);
		expect(imageReferences('nginx@sha256:abc')).toEqual(['nginx@sha256:abc']);
		expect(imageReferences('<none>:<none>')).toEqual([]);
	});
});

describe('imageJobs', () => {
	it('matches removal by ID and pulls of any of its tags', () => {
		const m = imageJobs('e1', {
			id: 'sha256:1',
			repoTags: ['nginx:latest'],
			repoDigests: ['nginx@sha256:9']
		});
		expect(matchJob(job('image.remove', [{ type: 'image', id: 'sha256:1' }]), m)).toBe(true);
		expect(matchJob(job('image.pull', [{ type: 'image', id: 'nginx' }]), m)).toBe(true);
		expect(
			matchJob(job('image.pull', [{ type: 'image', id: 'docker.io/library/nginx' }]), m)
		).toBe(true);
		expect(matchJob(job('image.pull', [{ type: 'image', id: 'nginx@sha256:9' }]), m)).toBe(
			true
		);
		expect(matchJob(job('image.pull', [{ type: 'image', id: 'nginx:1.27' }]), m)).toBe(false);
		expect(matchJob(job('image.pull', [{ type: 'image', id: 'nginx' }], 'e2'), m)).toBe(false);
	});
});

describe('kindJobs', () => {
	it('narrows to the selected environment, or none', () => {
		const pull = job('image.pull', [{ type: 'image', id: 'nginx' }], 'e2');
		expect(matchJob(pull, kindJobs(['image.pull'], null))).toBe(true);
		expect(matchJob(pull, kindJobs(['image.pull'], 'e1'))).toBe(false);
		expect(matchJob(pull, kindJobs(['image.pull'], 'e2'))).toBe(true);
		expect(matchJob(pull, kindJobs(['container.create'], null))).toBe(false);
	});
});
