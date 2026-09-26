import { describe, expect, it, vi } from 'vitest';
import { ApiRequestError } from '$lib/api/client';
import {
	StepUpCancelledError,
	StepUpPrompt,
	isStepUpRequired,
	withStepUp
} from '$lib/auth/stepup.svelte';
import { can, has, isDenied, isNotFound } from './access';
import { environmentName, fetchAllPages, ifMatch, newIdempotencyKey, shortDigest } from './data';
import { actionError, fieldErrors } from './errors';
import { SUGGESTED, defaultSchedule } from './schedules';
import { linesToList, listToLines } from './text';

const apiErr = (status: number, code: string, details: { field: string; message: string }[] = []) =>
	new ApiRequestError(code, status, {
		code,
		message: `${code} happened`,
		requestId: 'r',
		retryable: false,
		details
	});

describe('data helpers', () => {
	it('follows cursors until the last page', async () => {
		const page = vi.fn(async (cursor: string | undefined) =>
			cursor === undefined
				? { items: [1, 2], nextCursor: 'c2' }
				: cursor === 'c2'
					? { items: [3], nextCursor: undefined }
					: { items: [] }
		);
		expect(await fetchAllPages(page)).toEqual([1, 2, 3]);
		expect(page).toHaveBeenCalledTimes(2);
	});

	it('stops at the page bound', async () => {
		let n = 0;
		const page = async () => ({ items: [n++], nextCursor: 'more' });
		expect(await fetchAllPages(page, 3)).toEqual([0, 1, 2]);
	});

	it('formats If-Match, digests and environment names', () => {
		expect(ifMatch(7)).toBe('"7"');
		expect(shortDigest('sha256:0123456789abcdef0123')).toBe('0123456789ab');
		expect(shortDigest(undefined)).toBe('—');
		expect(environmentName([{ id: 'e1', name: 'homelab' }], 'e1')).toBe('homelab');
		expect(environmentName([], 'x')).toBe('Unknown environment');
		expect(newIdempotencyKey()).not.toBe(newIdempotencyKey());
	});

	it('turns textarea lines into lists and back', () => {
		expect(linesToList(' a=b \n\n c \n')).toEqual(['a=b', 'c']);
		expect(listToLines(['a', 'b'])).toBe('a\nb');
		expect(listToLines(undefined)).toBe('');
	});
});

describe('permission helpers', () => {
	it('shows actions the caller holds; the owner holds everything', () => {
		const a = { owner: false, allowed: new Set(['update_policy.manage']), environments: 1 };
		expect(can(a, 'update_policy.manage')).toBe(true);
		expect(can(a, 'maintenance.run')).toBe(false);
		expect(can({ ...a, owner: true }, 'maintenance.run')).toBe(true);
		expect(has({ actions: ['backup.run'] }, 'backup.run')).toBe(true);
		expect(has({ actions: [] }, 'backup.run')).toBe(false);
		expect(has(undefined, 'backup.run')).toBe(false);
	});

	it('recognizes denied and missing resources', () => {
		expect(isDenied(apiErr(403, 'forbidden'))).toBe(true);
		expect(isNotFound(apiErr(404, 'not_found'))).toBe(true);
		expect(isDenied(new Error('x'))).toBe(false);
	});
});

describe('error copy', () => {
	it('explains a stale edit and prefers per-screen overrides', () => {
		expect(actionError(apiErr(412, 'precondition_failed'))).toMatch(
			/Someone else changed this/
		);
		expect(
			actionError(apiErr(409, 'name_taken'), { name_taken: 'Choose a different name.' })
		).toBe('Choose a different name.');
		expect(actionError(apiErr(409, 'other'))).toBe('Other happened.');
		expect(
			fieldErrors(
				apiErr(422, 'validation_failed', [{ field: 'body.name', message: 'Too long.' }])
			)
		).toEqual({
			'body.name': 'Too long.'
		});
	});
});

describe('schedule defaults', () => {
	it('uses the instance defaults when readable, else the shipped suggestions (#13)', () => {
		expect(
			defaultSchedule('prune', {
				kinds: [
					{
						kind: 'prune',
						cron: '0 1 * * 6',
						catchUp: 'skip',
						label: 'Prune',
						suggested: '0 3 * * 0'
					}
				],
				revision: 1,
				timeZone: 'Europe/Berlin',
				updatedAt: ''
			})
		).toEqual({ cron: '0 1 * * 6', timeZone: 'Europe/Berlin' });
		expect(defaultSchedule('backup', undefined).cron).toBe(SUGGESTED.backup);
		expect(SUGGESTED).toMatchObject({
			backup: '0 * * * *',
			update_check: '0 3 * * *',
			update_run: '0 4 * * *',
			prune: '0 3 * * 0'
		});
	});
});

describe('step-up (#16)', () => {
	it('retries the change once after the user confirms their identity', async () => {
		const prompt = new StepUpPrompt();
		let calls = 0;
		const fn = async () => {
			calls++;
			if (calls === 1) throw apiErr(403, 'step_up_required');
			return 'done';
		};
		const p = withStepUp(fn, prompt);
		await vi.waitFor(() => expect(prompt.open).toBe(true));
		prompt.settle(true);
		expect(await p).toBe('done');
		expect(calls).toBe(2);
		expect(prompt.open).toBe(false);
	});

	it('gives up with a clear error when the check is dismissed', async () => {
		const prompt = new StepUpPrompt();
		const p = withStepUp(async () => {
			throw apiErr(403, 'step_up_required');
		}, prompt);
		await vi.waitFor(() => expect(prompt.open).toBe(true));
		prompt.settle(false);
		await expect(p).rejects.toBeInstanceOf(StepUpCancelledError);
	});

	it('passes other errors through without prompting', async () => {
		const prompt = new StepUpPrompt();
		await expect(
			withStepUp(async () => {
				throw apiErr(403, 'forbidden');
			}, prompt)
		).rejects.toMatchObject({ status: 403 });
		expect(prompt.open).toBe(false);
		expect(isStepUpRequired(apiErr(403, 'step_up_required'))).toBe(true);
	});
});
