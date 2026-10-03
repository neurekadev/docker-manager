// When the shell shows the "Live updates are disconnected" banner (#22):
// only after the live stream (#23, `liveStatus`) has been down — retrying
// or polling — for OFFLINE_BANNER_DELAY_MS, so a short reconnect never
// flashes a banner. Pure, so the timing is unit-tested.
import type { LiveState } from '$lib/live/status.svelte';

export const OFFLINE_BANNER_DELAY_MS = 5000;

export function isDown(state: LiveState): boolean {
	return state === 'reconnecting' || state === 'polling';
}

/**
 * Milliseconds until the banner is due (0: show it now), or null when the
 * stream is not down (hide it).
 */
export function bannerDelay(state: LiveState, since: number, now: number): number | null {
	if (!isDown(state)) return null;
	return Math.max(0, since + OFFLINE_BANNER_DELAY_MS - now);
}

/** The banner's title and text: why live updates stopped, when known. */
export function bannerText(tooManyStreams: boolean): { title: string; body: string } {
	return tooManyStreams
		? {
				title: 'Too many Docker Manager tabs are open',
				body: 'Live updates stopped here. Close tabs you no longer need.'
			}
		: {
				title: 'Live updates are disconnected',
				body: 'Pages may be out of date until it reconnects.'
			};
}

/** Text of the top-bar indicator: nothing while live or not connected at all. */
export function indicatorText(state: LiveState): string {
	switch (state) {
		case 'connecting':
			return 'Connecting…';
		case 'reconnecting':
			return 'Reconnecting…';
		case 'polling':
			return 'Live updates paused';
		default:
			return '';
	}
}
