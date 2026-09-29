// After "Resume on this server" (or cancelling a move whose agents heard
// the new address) Docker Manager raises its generation and restarts
// (docs/internal/architecture/manager-move.md). The page waits until it
// answers again and then reloads: first until it stops answering (the
// restart begins once the request that asked for it finished; a quick
// restart may never be seen down), then until it answers again, with
// backoff. Pure: the probe, the sleep and the clock are passed in
// (restart.spec.ts).

/** How long to look for the manager going down before waiting for it to answer (ms). */
export const RESTART_DOWN_WINDOW_MS = 15_000;
/** How long to wait in all before giving up and offering Reload (ms). */
export const RESTART_TIMEOUT_MS = 180_000;
/** The first and the longest pause between two probes (ms). */
export const RESTART_POLL_MIN_MS = 500;
export const RESTART_POLL_MAX_MS = 5_000;

export interface RestartDeps {
	/** true when Docker Manager answered (GET /api/v1/health). */
	probe: () => Promise<boolean>;
	sleep: (ms: number) => Promise<void>;
	now: () => number;
	/** Stops the wait (the page went away). */
	signal?: AbortSignal;
}

export type RestartOutcome = 'up' | 'timeout' | 'aborted';

/** Waits until Docker Manager restarted and answers again. */
export async function waitForRestart(d: RestartDeps): Promise<RestartOutcome> {
	const start = d.now();
	const elapsed = () => d.now() - start;
	// 1. Until it stops answering (or the window passed).
	while (elapsed() < RESTART_DOWN_WINDOW_MS) {
		if (d.signal?.aborted) return 'aborted';
		if (!(await d.probe())) break;
		await d.sleep(RESTART_POLL_MIN_MS);
	}
	// 2. Until it answers again, with backoff.
	let pause = RESTART_POLL_MIN_MS;
	while (elapsed() < RESTART_TIMEOUT_MS) {
		if (d.signal?.aborted) return 'aborted';
		if (await d.probe()) return 'up';
		await d.sleep(pause);
		pause = Math.min(pause * 2, RESTART_POLL_MAX_MS);
	}
	return 'timeout';
}
