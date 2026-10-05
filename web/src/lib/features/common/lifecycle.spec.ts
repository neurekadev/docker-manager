import { describe, expect, it } from 'vitest';
import { lifecycleEntries, lifecycleMain, type LifecycleActions } from './lifecycle';

const run = () => {};
const all: LifecycleActions = { start: { run }, restart: { run }, stop: { run } };

describe('lifecycle button model', () => {
	it('makes Stop the main action while anything runs and Start while nothing does', () => {
		expect(lifecycleMain(true, all)).toBe('stop');
		expect(lifecycleMain(false, all)).toBe('start');
	});

	it('falls back to the actions the caller holds', () => {
		expect(lifecycleMain(true, { start: { run }, restart: { run } })).toBe('restart');
		// A partially running stack without stop and restart: Start starts the rest.
		expect(lifecycleMain(true, { start: { run } })).toBe('start');
		// All runs and only Start is held: nothing to offer.
		expect(lifecycleMain(true, { start: { run, disabled: true } })).toBeUndefined();
		expect(lifecycleMain(false, { restart: { run }, stop: { run } })).toBeUndefined();
		expect(lifecycleMain(true, {})).toBeUndefined();
	});

	it('keeps a Stop that is off as the main action (its reason shows)', () => {
		expect(lifecycleMain(true, { ...all, stop: { run, disabled: true, reason: 'why' } })).toBe(
			'stop'
		);
	});

	it('lists Start, Restart and Stop in that order, only the held ones', () => {
		expect(
			lifecycleEntries({ stop: { run }, start: { run }, restart: { run } }).map(
				(e) => e.label
			)
		).toEqual(['Start', 'Restart', 'Stop']);
		expect(lifecycleEntries({ stop: { run } }).map((e) => e.verb)).toEqual(['stop']);
		expect(lifecycleEntries({})).toEqual([]);
	});

	it("lists a stack's Take Down last and makes it the main action only when nothing else applies", () => {
		expect(lifecycleEntries({ ...all, down: { run } }).map((e) => e.label)).toEqual([
			'Start',
			'Restart',
			'Stop',
			'Take Down'
		]);
		expect(lifecycleMain(true, { ...all, down: { run } })).toBe('stop');
		expect(lifecycleMain(false, { ...all, down: { run } })).toBe('start');
		expect(lifecycleMain(true, { down: { run } })).toBe('down');
		expect(lifecycleMain(false, { down: { run } })).toBe('down');
		expect(lifecycleMain(true, { down: { run, disabled: true } })).toBeUndefined();
	});

	it('turns off what does not apply', () => {
		const entries = lifecycleEntries({
			start: { run, disabled: true },
			restart: { run, disabled: true, reason: 'Protected' },
			stop: { run }
		});
		expect(entries).toEqual([
			{ verb: 'start', label: 'Start', disabled: true },
			{ verb: 'restart', label: 'Restart', disabled: true },
			{ verb: 'stop', label: 'Stop', disabled: false }
		]);
		expect(lifecycleEntries(all, true).every((e) => e.disabled)).toBe(true);
	});
});
