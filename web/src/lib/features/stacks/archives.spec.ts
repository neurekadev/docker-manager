// Stack archives (#313): what the export dialog offers per volume and
// sends, the check's headline, the downtime notice, the newest archive in
// words, and the choices of a stack created from an archive.
import { describe, expect, it } from 'vitest';
import {
	NOT_PERMITTED,
	archiveContents,
	archiveFileText,
	archiveHeadline,
	defaultName,
	exclusionText,
	exportBlocker,
	exportBody,
	exportDownloadUrl,
	exportDowntime,
	exportKey,
	importBlocker,
	importBody,
	importKey,
	includeChoice,
	limitRelevant,
	notPermittedKeys,
	renamedVolumes,
	type StackArchiveVolume
} from './archives';

const vol = (over: Partial<StackArchiveVolume> = {}): StackArchiveVolume => ({
	key: 'data',
	name: 'silo_data',
	included: true,
	bytes: 1000,
	entries: 10,
	...over
});

describe('export', () => {
	it('downloads an export by its ID', () => {
		expect(exportDownloadUrl('st 1', 'job/1')).toBe('/api/v1/stacks/st%201/exports/job%2F1');
	});

	it('sends the volumes left out once each and in order, nothing when none', () => {
		expect(exportBody([])).toEqual({});
		expect(exportBody(['media', 'cache', 'media'])).toEqual({
			excludeVolumes: ['cache', 'media']
		});
		expect(exportKey(['b', 'a'])).toBe(exportKey(['a', 'b', 'a']));
		expect(exportKey(['a'])).not.toBe(exportKey([]));
	});

	it('leaves out the volumes the user may not download from the start', () => {
		expect(
			notPermittedKeys([
				vol({ key: 'data' }),
				vol({ key: 'secret', included: false, notPermitted: true }),
				vol({ key: 'ext', included: false, reason: 'external volume' })
			])
		).toEqual(['secret']);
	});

	it('offers Include for plain volumes and says why the others cannot be included', () => {
		expect(includeChoice(vol(), [])).toEqual({ checked: true, disabled: false });
		expect(includeChoice(vol({ included: false, excluded: true }), ['data'])).toEqual({
			checked: false,
			disabled: false
		});
		expect(
			includeChoice(vol({ included: false, notPermitted: true, excluded: true }), ['data'])
		).toEqual({ checked: false, disabled: true, reason: NOT_PERMITTED });
		expect(
			includeChoice(
				vol({
					included: false,
					reason: 'external volume: it must exist where the stack is created'
				}),
				[]
			)
		).toEqual({
			checked: false,
			disabled: true,
			reason: 'External volume: it must exist where the stack is created.'
		});
	});

	it('sums up the check in one line', () => {
		const f = { code: 'x', message: 'y' };
		expect(archiveHeadline({ blockers: [f, f], warnings: [f] }, 'export')).toEqual({
			tone: 'danger',
			title: '2 problems must be fixed before the stack can be exported.'
		});
		expect(archiveHeadline({ blockers: [], warnings: [f] }, 'export')).toEqual({
			tone: 'warn',
			title: 'Ready to export, with 1 warning.'
		});
		expect(archiveHeadline({ blockers: [], warnings: [] }, 'create')).toEqual({
			tone: 'info',
			title: 'Ready to create the stack.'
		});
		expect(archiveHeadline({ blockers: [f], warnings: [] }, 'create').title).toBe(
			'1 problem must be fixed before the stack can be created.'
		);
	});

	it('warns of the downtime only when something runs', () => {
		expect(exportDowntime('Silo', { running: [], downtimeSeconds: 0 })).toBeNull();
		expect(exportDowntime('Silo', { running: ['web'], downtimeSeconds: 150 })).toEqual({
			title: 'Silo stops while the archive is written and starts again afterwards.',
			body: 'Expected downtime: about 3 min.'
		});
	});

	it('shows the archive limit only when the data comes close to it', () => {
		expect(limitRelevant({ totalBytes: 100, maxBytes: 1000 })).toBe(false);
		expect(limitRelevant({ totalBytes: 800, maxBytes: 1000 })).toBe(true);
		expect(limitRelevant({ totalBytes: 2000, maxBytes: 1000 })).toBe(true);
		expect(limitRelevant({ totalBytes: 2000, maxBytes: 0 })).toBe(false);
	});

	it('names the newest archive with its size and when it goes away', () => {
		expect(
			archiveFileText(
				{
					fileName: 'silo-2026-10-10.tar.gz',
					size: 1_572_864,
					expiresAt: '2026-10-11T09:30:00Z'
				},
				'UTC'
			)
		).toBe('silo-2026-10-10.tar.gz · 1.5 MB · Available until Oct 11, 2026, 09:30');
	});

	it('says what was left out and why', () => {
		expect(
			exclusionText({
				kind: 'bind',
				name: '/srv/media',
				reason: 'outside the project folder'
			})
		).toEqual({ what: 'Bind mount', reason: 'Outside the project folder.' });
		expect(
			exclusionText({ kind: 'anonymous_volume', name: 'abc', reason: 'anonymous volume' })
		).toMatchObject({ what: 'Anonymous volume' });
	});
});

describe('create from an archive', () => {
	it('starts with the name the Compose file pins, else the stack’s', () => {
		expect(defaultName({ name: 'silo' })).toBe('silo');
		expect(defaultName({ name: 'silo', pinnedName: 'cloud' })).toBe('cloud');
	});

	it('counts the volumes and the data of the archive', () => {
		expect(
			archiveContents({
				projectBytes: 524_288,
				volumes: [
					{ key: 'a', name: 'silo_a', bytes: 1_048_576, entries: 1 },
					{ key: 'b', name: 'silo_b', bytes: 524_288, entries: 1 }
				]
			})
		).toBe('2 volumes · 2 MB');
		expect(archiveContents({ projectBytes: 1024, volumes: [] })).toBe('0 volumes · 1 KB');
	});

	it('sends the choice trimmed, without an empty display name or a deploy not asked for', () => {
		const c = { environmentId: 'env-1', name: ' web ', displayName: '  ', deploy: false };
		expect(importBody(c)).toEqual({
			environmentId: 'env-1',
			name: 'web',
			displayName: undefined,
			deploy: undefined
		});
		expect(importBody({ ...c, displayName: 'Web', deploy: true })).toMatchObject({
			displayName: 'Web',
			deploy: true
		});
	});

	it('checks again when the environment, the name or the deploy change, not the display name', () => {
		const c = { environmentId: 'env-1', name: 'web', displayName: '', deploy: true };
		expect(importKey('a-1', c)).toBe(importKey('a-1', { ...c, displayName: 'Web' }));
		expect(importKey('a-1', c)).not.toBe(importKey('a-1', { ...c, name: 'web2' }));
		expect(importKey('a-1', c)).not.toBe(importKey('a-1', { ...c, environmentId: 'env-2' }));
		expect(importKey('a-1', c)).not.toBe(importKey('a-1', { ...c, deploy: false }));
	});

	it('lists the volumes whose names follow the new stack name', () => {
		expect(
			renamedVolumes([
				{ key: 'data', source: 'silo_data', name: 'cloud_data', bytes: 1 },
				{ key: 'shared', source: 'shared', name: 'shared', bytes: 1 }
			])
		).toEqual([{ key: 'data', source: 'silo_data', name: 'cloud_data', bytes: 1 }]);
	});
});

describe('blockers', () => {
	const ok = { allowed: true, blockers: [] };
	const blocked = { allowed: false, blockers: [{ code: 'x', message: 'y' }] };

	it('keeps Export Archive off while checking and with problems', () => {
		expect(exportBlocker({ preview: null, checking: true, stale: false })).toBe('Checking…');
		expect(exportBlocker({ preview: ok, checking: false, stale: true })).toBe('Checking…');
		expect(exportBlocker({ preview: blocked, checking: false, stale: false })).toBe(
			'Fix the problem first.'
		);
		expect(exportBlocker({ preview: ok, checking: false, stale: false })).toBeUndefined();
	});

	it('keeps Create Stack off without an environment, with a bad name, offline or with problems', () => {
		const c = {
			preview: ok,
			checking: false,
			stale: false,
			environment: { name: 'nas', online: true },
			name: 'web'
		};
		expect(importBlocker(c)).toBeUndefined();
		expect(importBlocker({ ...c, environment: undefined })).toBe(
			'Choose an environment for the stack.'
		);
		expect(importBlocker({ ...c, name: 'Web!' })).toBe('Fix the name to continue.');
		expect(importBlocker({ ...c, environment: { name: 'nas', online: false } })).toBe(
			'nas is offline. Create the stack when it is back.'
		);
		expect(importBlocker({ ...c, preview: { allowed: false, blockers: [1, 2] } })).toBe(
			'Fix the problems first.'
		);
		expect(importBlocker({ ...c, preview: { allowed: false, blockers: [] } })).toBe(
			'The stack cannot be created now.'
		);
	});
});
