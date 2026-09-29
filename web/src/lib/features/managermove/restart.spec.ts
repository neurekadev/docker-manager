// The wait for Docker Manager's restart after a resume: down first, then
// up again with backoff; a restart too quick to be seen down; giving up.
import { describe, expect, it } from 'vitest';
import {
	RESTART_DOWN_WINDOW_MS,
	RESTART_POLL_MAX_MS,
	RESTART_POLL_MIN_MS,
	RESTART_TIMEOUT_MS,
	waitForRestart,
	type RestartDeps
} from './restart';

/** A fake clock and a manager that answers per `up(t)`. */
function fake(up: (t: number) => boolean, signal?: AbortSignal) {
	let t = 0;
	const probes: number[] = [];
	const pauses: number[] = [];
	const deps: RestartDeps = {
		probe: async () => {
			probes.push(t);
			return up(t);
		},
		sleep: async (ms) => {
			pauses.push(ms);
			t += ms;
		},
		now: () => t,
		signal
	};
	return { deps, probes, pauses };
}

describe('waiting for the restart', () => {
	it('waits until the manager went down and answers again', async () => {
		const f = fake((t) => t < 2_000 || t >= 9_000);
		expect(await waitForRestart(f.deps)).toBe('up');
		// Down at 2 s; then backoff 0.5, 1, 2, 4 s: up again at 9.5 s.
		expect(f.probes.at(-1)).toBe(9_500);
		expect(f.pauses.slice(4)).toEqual([500, 1_000, 2_000, 4_000]);
	});

	it('does not wait forever for a restart too quick to be seen', async () => {
		const f = fake(() => true);
		expect(await waitForRestart(f.deps)).toBe('up');
		expect(f.probes.at(-1)).toBe(RESTART_DOWN_WINDOW_MS);
		expect(f.pauses.every((p) => p === RESTART_POLL_MIN_MS)).toBe(true);
	});

	it('gives up after the timeout, never pausing longer than the maximum', async () => {
		const f = fake(() => false);
		expect(await waitForRestart(f.deps)).toBe('timeout');
		expect(Math.max(...f.pauses)).toBe(RESTART_POLL_MAX_MS);
		expect(f.probes.at(-1)).toBeLessThan(RESTART_TIMEOUT_MS);
	});

	it('stops when the page goes away', async () => {
		const ctl = new AbortController();
		ctl.abort();
		expect(await waitForRestart(fake(() => false, ctl.signal).deps)).toBe('aborted');
	});
});
