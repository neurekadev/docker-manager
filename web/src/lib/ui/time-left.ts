// The time left of a running job from its percent (the file manager's
// archive and extraction jobs): the rate since the view first saw the job
// progress, so a job followed again after a reload estimates from then
// on. Pure; tested in time-left.spec.ts.
import { formatDuration } from './format';

/** Where and when a job's percent was first seen. */
export interface ProgressMark {
	/** Milliseconds (Date.now()). */
	at: number;
	percent: number;
}

/** Less time than this since the mark tells too little. */
export const MIN_ELAPSED_MS = 2000;

/**
 * The seconds left at `percent` at time `now` (ms), from the rate since
 * `mark`; undefined until it can be told (no mark, too soon, no progress
 * since) and once done.
 */
export function secondsLeft(
	mark: ProgressMark | undefined,
	now: number,
	percent: number | undefined
): number | undefined {
	if (!mark || percent === undefined || percent >= 100) return undefined;
	const elapsed = now - mark.at;
	const gained = percent - mark.percent;
	if (elapsed < MIN_ELAPSED_MS || gained <= 0) return undefined;
	return ((100 - percent) * elapsed) / gained / 1000;
}

/**
 * "About 2 min left": steady words for an estimate that changes every
 * second (under a minute in steps of 5 s, then whole minutes).
 */
export function timeLeftText(seconds: number): string {
	const s =
		seconds < 60
			? Math.max(5, Math.ceil(seconds / 5) * 5)
			: Math.max(1, Math.round(seconds / 60)) * 60;
	return `About ${formatDuration(s)} left`;
}
