import { describe, expect, it } from 'vitest';
import {
	canRename,
	containerLabel,
	lockedName,
	renameConsequences,
	renameNameError,
	stackJobGuidance,
	volumeLabel,
	volumeNote,
	type RenamePreview
} from './rename';

function preview(over: Partial<RenamePreview> = {}): RenamePreview {
	return {
		from: 'shop',
		to: 'store',
		fromDir: 'shop',
		toDir: 'store',
		running: ['web', 'db'],
		volumes: [
			{ key: 'data', name: 'shop_data', newName: 'store_data', action: 'move' },
			{ key: 'media', name: 'shop_media', newName: 'store_media', action: 'recreate' },
			{ key: 'cache', name: 'shop_cache', newName: 'store_cache', action: 'absent' }
		],
		containers: [{ id: 'c1', name: 'backup', running: true, volumes: ['shop_data'] }],
		blockers: [],
		warnings: [],
		...over
	};
}

describe('stack rename model', () => {
	it('checks the new project name', () => {
		expect(renameNameError('', 'shop')).toBe('Enter a name.');
		expect(renameNameError('Store', 'shop')).toMatch(/lower-case/);
		expect(renameNameError('-store', 'shop')).toMatch(/lower-case/);
		expect(renameNameError('shop', 'shop')).toBe('The stack already has this name.');
		expect(renameNameError('store_2-b', 'shop')).toBeUndefined();
	});

	it('locks the name to the one the Compose file declares', () => {
		expect(lockedName('shop', undefined)).toBeUndefined();
		expect(lockedName('shop', { declaredName: undefined })).toBeUndefined();
		// name: equal to the current name: renamed by editing the file (a blocker).
		expect(lockedName('shop', { declaredName: 'shop' })).toBeUndefined();
		expect(lockedName('shop', { declaredName: 'store' })).toBe('store');
	});

	it('says what happens to each volume and container', () => {
		const p = preview();
		expect(p.volumes.map(volumeNote)).toEqual([
			'Its data moves to the new name.',
			'Same host path or remote storage under the new name; the data stays where it is.',
			'Does not exist yet; created on the first start.'
		]);
		expect(volumeLabel(p.volumes[0])).toBe('shop_data → store_data');
		expect(
			volumeLabel({ name: 'abc123', service: 'db', target: '/var/lib/db', action: 'move' })
		).toBe('db:/var/lib/db');
		expect(containerLabel(p.containers[0])).toBe('backup');
		expect(containerLabel({ hidden: true, running: false, volumes: [] })).toBe(
			'A container you cannot see'
		);
	});

	it('lists the consequences', () => {
		expect(renameConsequences(preview())).toEqual([
			'Stops 2 running services (web, db) and starts them again as store.',
			'Moves 2 volumes to the new name.',
			'Renames the project folder shop to store.',
			'Stops and recreates 1 container outside the stack that uses these volumes.',
			'The stack keeps its history, policies and permissions.'
		]);
		expect(
			renameConsequences(preview({ running: [], volumes: [], containers: [], toDir: 'shop' }))
		).toEqual([
			'No service runs now; the containers are recreated as store and stay stopped.',
			'Keeps the project folder shop.',
			'The stack keeps its history, policies and permissions.'
		]);
	});

	it('confirms only the previewed name without blockers', () => {
		expect(canRename('store', 'shop', undefined)).toBe(false);
		expect(canRename('store', 'shop', preview())).toBe(true);
		expect(canRename('storage', 'shop', preview())).toBe(false);
		expect(
			canRename(
				'store',
				'shop',
				preview({
					blockers: [{ code: 'target_volume_exists', message: 'store_data exists' }]
				})
			)
		).toBe(false);
	});

	it('explains a deploy refused because the files set another project name', () => {
		expect(stackJobGuidance({ class: 'stack_project_renamed', recovery: 'x' })).toMatch(
			/Rename the stack to that name/
		);
		expect(stackJobGuidance({ class: 'engine_error', message: 'm', recovery: 'r' })).toBe('r');
		expect(stackJobGuidance({ class: 'engine_error', message: 'm' })).toBe('m');
		expect(stackJobGuidance(undefined)).toBeUndefined();
	});
});
