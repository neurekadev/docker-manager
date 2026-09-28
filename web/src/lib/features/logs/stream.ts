// Container log stream contract (#8, docs/internal/api/streams.md "Container logs"):
// the followed containers, the stream URL, the cursor order and the end
// reasons. The LogFeed (feed.svelte.ts) builds on these.
import type { TileColor } from '$lib/design/hue';

export interface LogSource {
	/** Stable key (the container name). */
	key: string;
	environmentId: string;
	containerId: string;
	/** Compose service (stack logs). */
	service?: string;
	label: string;
	/** The service's hue (serviceHue): its log prefix and chart series colour. */
	color?: TileColor;
}

/**
 * A sortable form of an RFC 3339 timestamp: Go trims trailing zeros of
 * the fraction ("…:00Z" before "…:00.5Z"), so strings do not compare in
 * time order until the fraction is padded to nanoseconds.
 */
export function timeKey(at: string): string {
	const m = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(at);
	if (!m) return at;
	if (m[3] !== 'Z') {
		// The manager sends UTC; an offset falls back to millisecond precision.
		const d = new Date(at);
		return Number.isNaN(d.getTime()) ? at : timeKey(d.toISOString());
	}
	return `${m[1]}.${(m[2] ?? '').padEnd(9, '0')}Z`;
}

/** The stream URL: a first open reads `tail` lines of history; a resume starts at `since`. */
export function streamUrl(s: LogSource, tail: number, since?: string): string {
	const e = encodeURIComponent;
	const q = new URLSearchParams();
	if (since) q.set('since', since);
	else q.set('tail', String(tail));
	return `/api/v1/environments/${e(s.environmentId)}/containers/${e(s.containerId)}/logs/stream?${q}`;
}

/** Plain-language reason of an `end` event. */
export function endReason(reason: string): string {
	switch (reason) {
		case 'container_removed':
			return 'The container was removed.';
		case 'permissions_changed':
			return 'Your permissions changed. Reopen the logs to continue with your current access.';
		case 'agent_offline':
			return 'The environment went offline. The logs continue when it reconnects.';
		case 'session_expired':
			return 'Your session ended. Sign in again to continue.';
		default:
			return 'The log stream ended.';
	}
}
