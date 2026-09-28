import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import {
	activeRename,
	lockedName,
	renameNameError,
	renameRefusal,
	stackJobGuidance
} from './rename';

const job = (kind: string, state: string) => ({ id: `${kind}-${state}`, kind, state }) as Job;

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

	it('says why the server would refuse the rename', () => {
		expect(renameRefusal('store', 'shop', { blockers: [] })).toBeUndefined();
		// The Compose file's name: fixes the project name.
		expect(renameRefusal('other', 'shop', { declaredName: 'store', blockers: [] })).toBe(
			'Its Compose file sets name: store. The stack can only take that name; to choose another, change name: in the file.'
		);
		expect(
			renameRefusal('store', 'shop', { declaredName: 'store', blockers: [] })
		).toBeUndefined();
		expect(
			renameRefusal('store', 'shop', {
				blockers: [
					{ code: 'target_volume_exists', message: 'The volume store_data exists.' },
					{ code: 'name_taken', message: 'Another stack is named store.' }
				]
			})
		).toBe('The volume store_data exists. Another stack is named store.');
	});

	it('finds a rename that has not ended', () => {
		expect(activeRename(undefined)).toBeUndefined();
		expect(activeRename([job('stack.deploy', 'running')])).toBeUndefined();
		expect(activeRename([job('stack.rename', 'succeeded')])).toBeUndefined();
		expect(activeRename([job('stack.rename', 'failed')])).toBeUndefined();
		for (const state of ['queued', 'blocked', 'dispatched', 'running', 'cancelling'])
			expect(activeRename([job('stack.rename', state)])?.state).toBe(state);
	});

	it('explains a deploy refused because the files set another project name', () => {
		expect(stackJobGuidance({ class: 'stack_project_renamed', recovery: 'x' })).toMatch(
			/Rename the stack to that name with the pencil next to its name/
		);
		expect(stackJobGuidance({ class: 'engine_error', message: 'm', recovery: 'r' })).toBe('r');
		expect(stackJobGuidance({ class: 'engine_error', message: 'm' })).toBe('m');
		expect(stackJobGuidance(undefined)).toBeUndefined();
	});
});
