import { describe, expect, it } from 'vitest';
import {
	accessChangeText,
	canCopyImage,
	checkHeadline,
	copiedVolumes,
	count,
	imageActionLabel,
	migrationBody,
	migrationTargets,
	selectionChanged,
	selectionKey,
	sentence,
	toggled,
	volumeActionLabel,
	volumeChoice,
	type MigrationSelection,
	type ServicePlan,
	type VolumePlan
} from './migration';

const sel = (over: Partial<MigrationSelection> = {}): MigrationSelection => ({
	target: 'env-b',
	excluded: [],
	anonymous: [],
	transfer: [],
	...over
});

const vol = (over: Partial<VolumePlan> = {}): VolumePlan => ({
	source: 'silo_data',
	target: 'silo_data',
	key: 'data',
	action: 'copy',
	bytes: 1000,
	entries: 3,
	...over
});

const svc = (over: Partial<ServicePlan> = {}): ServicePlan => ({
	name: 'web',
	image: 'nginx:1.27',
	action: 'pull',
	...over
});

describe('migrationBody', () => {
	it('sends only the lists that have entries', () => {
		expect(migrationBody(sel())).toEqual({
			targetEnvironmentId: 'env-b',
			excludeVolumes: undefined,
			anonymousVolumes: undefined,
			transferImages: undefined
		});
		expect(
			migrationBody(sel({ excluded: ['a'], anonymous: ['b'], transfer: ['nginx:1.27'] }))
		).toEqual({
			targetEnvironmentId: 'env-b',
			excludeVolumes: ['a'],
			anonymousVolumes: ['b'],
			transferImages: ['nginx:1.27']
		});
	});
});

describe('selectionKey and selectionChanged', () => {
	it('ignores the order of the ticks and duplicates', () => {
		expect(selectionKey(sel({ excluded: ['a', 'b'] }))).toBe(
			selectionKey(sel({ excluded: ['b', 'a', 'a'] }))
		);
	});

	it('tells the lists apart', () => {
		expect(selectionKey(sel({ excluded: ['a'] }))).not.toBe(
			selectionKey(sel({ anonymous: ['a'] }))
		);
		expect(selectionKey(sel({ target: 'env-c' }))).not.toBe(selectionKey(sel()));
	});

	it('is stale only when a check ran for another selection', () => {
		const checked = selectionKey(sel({ transfer: ['nginx:1.27'] }));
		expect(selectionChanged(null, sel())).toBe(false);
		expect(selectionChanged(checked, sel({ transfer: ['nginx:1.27'] }))).toBe(false);
		expect(selectionChanged(checked, sel())).toBe(true);
		expect(selectionChanged(checked, sel({ transfer: ['nginx:1.27'], excluded: ['x'] }))).toBe(
			true
		);
	});
});

describe('toggled', () => {
	it('adds once and removes', () => {
		expect(toggled(['a'], 'b', true)).toEqual(['a', 'b']);
		expect(toggled(['a', 'b'], 'b', true)).toEqual(['a', 'b']);
		expect(toggled(['a', 'b'], 'a', false)).toEqual(['b']);
		expect(toggled([], 'a', false)).toEqual([]);
	});
});

describe('copiedVolumes', () => {
	const volumes = [
		vol({ source: 'silo_data' }),
		vol({ source: 'silo_media', key: 'media' }),
		vol({ source: 'silo_cache', action: 'skip' }),
		vol({ source: 'abc123', key: undefined, anonymous: true, action: 'copy' }),
		vol({ source: 'shared', action: 'external' })
	];

	it("lists the check's copies", () => {
		expect(
			copiedVolumes(volumes, { excluded: [], anonymous: ['abc123'] }).map((v) => v.source)
		).toEqual(['silo_data', 'silo_media', 'abc123']);
	});

	it('leaves out named volumes excluded since the check', () => {
		expect(
			copiedVolumes(volumes, { excluded: ['silo_media'], anonymous: ['abc123'] }).map(
				(v) => v.source
			)
		).toEqual(['silo_data', 'abc123']);
	});

	it('leaves out anonymous volumes no longer chosen', () => {
		expect(
			copiedVolumes(volumes, { excluded: [], anonymous: [] }).map((v) => v.source)
		).toEqual(['silo_data', 'silo_media']);
	});
});

describe('canCopyImage', () => {
	it('offers the choice for pulled images and keeps it once chosen', () => {
		expect(canCopyImage(svc(), [])).toBe(true);
		expect(canCopyImage(svc({ action: 'transfer' }), ['nginx:1.27'])).toBe(true);
		expect(canCopyImage(svc({ action: 'transfer' }), [])).toBe(false);
		expect(canCopyImage(svc({ action: 'rebuild' }), [])).toBe(false);
		expect(canCopyImage(svc({ action: 'present' }), [])).toBe(false);
	});
});

describe('volumeChoice', () => {
	it('offers the right choice per volume', () => {
		expect(volumeChoice(vol({ anonymous: true, action: 'skip' }), [])).toBe('anonymous');
		expect(volumeChoice(vol(), [])).toBe('named');
		expect(volumeChoice(vol({ action: 'skip' }), ['silo_data'])).toBe('named');
		expect(volumeChoice(vol({ action: 'skip' }), [])).toBeNull();
		expect(volumeChoice(vol({ action: 'external' }), [])).toBeNull();
		expect(volumeChoice(vol({ action: 'definition_only' }), [])).toBeNull();
	});
});

describe('labels', () => {
	it('reads the plan in words, never the enum key', () => {
		expect(imageActionLabel('pull')).toBe('Downloaded on the destination');
		expect(imageActionLabel('transfer')).toBe('Copied through Docker Manager');
		expect(imageActionLabel('something_new')).toBe('Handled on the destination');
		expect(volumeActionLabel('copy')).toBe('Data copied');
		expect(volumeActionLabel('skip')).toBe('Created empty');
		expect(volumeActionLabel('external')).toBe('Must already exist there');
		expect(volumeActionLabel('something_new')).toBe('Not copied');
	});

	it('counts', () => {
		expect(count(1, 'volume')).toBe('1 volume');
		expect(count(0, 'volume')).toBe('0 volumes');
		expect(count(2, 'service')).toBe('2 services');
	});

	it('capitalises a sentence', () => {
		expect(sentence('the copy takes 3 s')).toBe('The copy takes 3 s');
		expect(sentence('')).toBe('');
	});
});

describe('checkHeadline', () => {
	const f = { code: 'port_conflict', message: 'port 80 is in use' };

	it('leads with the problems to fix', () => {
		expect(checkHeadline({ blockers: [f], warnings: [f] })).toEqual({
			tone: 'danger',
			title: '1 problem must be fixed before the stack can move.'
		});
		expect(checkHeadline({ blockers: [f, f], warnings: [] }).title).toBe(
			'2 problems must be fixed before the stack can move.'
		);
	});

	it('says that starting accepts the warnings', () => {
		expect(checkHeadline({ blockers: [], warnings: [f] })).toEqual({
			tone: 'warn',
			title: 'Ready to move, with 1 warning. Starting the migration accepts it.'
		});
		expect(checkHeadline({ blockers: [], warnings: [f, f] }).title).toBe(
			'Ready to move, with 2 warnings. Starting the migration accepts them.'
		);
	});

	it('is ready without findings', () => {
		expect(checkHeadline({ blockers: [], warnings: [] })).toEqual({
			tone: 'info',
			title: 'Ready to move. Nothing needs your attention.'
		});
	});
});

describe('accessChangeText', () => {
	it('joins gains and losses', () => {
		expect(accessChangeText({ gained: ['Deploy'], lost: [] })).toBe('gains Deploy');
		expect(accessChangeText({ gained: [], lost: ['Delete', 'Rename'] })).toBe(
			'loses Delete, Rename'
		);
		expect(accessChangeText({ gained: ['Deploy'], lost: ['Delete'] })).toBe(
			'gains Deploy; loses Delete'
		);
	});
});

describe('migrationTargets', () => {
	it('offers every other active environment', () => {
		const envs = [
			{ id: 'a', status: 'active' },
			{ id: 'b', status: 'active' },
			{ id: 'c', status: 'archived' }
		];
		expect(migrationTargets(envs, 'a').map((e) => e.id)).toEqual(['b']);
		expect(migrationTargets([{ id: 'a', status: 'active' }], 'a')).toEqual([]);
		expect(migrationTargets(undefined, 'a')).toEqual([]);
	});
});
