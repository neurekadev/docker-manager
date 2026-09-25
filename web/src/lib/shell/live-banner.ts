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
