// Registry pull limits (#217): what a registry last reported to Docker
// Manager's checks, per registry host and credential, in words for the
// Registries page. Pure; `now` is injectable for tests.
import type { RegistryPullLimit } from '$lib/api/queries';
import { formatDateTime, formatDuration, formatRelative } from '$lib/ui/format';

/** Below this share of the limit left, the figure warns. */
export const LOW_SHARE = 0.1;

export interface PullLimitView {
	/** "187 of 200 left", "Limit reached", "Not reported". */
	text: string;
	/** "per 6 hours · checked 4 minutes ago", "resets in 12 minutes". */
	sub: string;
	tone: 'normal' | 'warn' | 'muted';
	/** The exact time the figure is from (tooltip). */
	title: string;
}

/** A limit's window in words: "hour", "6 hours", "day", "90 s". */
export function windowLabel(seconds: number): string {
	const units: [number, string][] = [
		[86400, 'day'],
		[3600, 'hour'],
		[60, 'minute']
	];
	for (const [size, unit] of units) {
		if (seconds >= size && seconds % size === 0) {
			const n = seconds / size;
			return n === 1 ? unit : `${n} ${unit}s`;
		}
	}
	return formatDuration(seconds);
}

/** How a stored pull limit reads; null when the credential was never checked. */
export function pullLimitView(
	p: RegistryPullLimit | undefined,
	now: Date = new Date()
): PullLimitView | null {
	if (!p) return null;
	if (p.limited) {
		const sub = p.limitedUntil
			? `resets ${formatRelative(p.limitedUntil, now)}`
			: `checked ${formatRelative(p.checkedAt, now)}`;
		return {
			text: 'Limit reached',
			sub,
			tone: 'warn',
			title: formatDateTime(p.lastLimitedAt ?? p.checkedAt)
		};
	}
	if (p.limit === undefined) {
		return {
			text: 'Not reported',
			sub: `checked ${formatRelative(p.checkedAt, now)}`,
			tone: 'muted',
			title: formatDateTime(p.checkedAt)
		};
	}
	const at = p.observedAt ?? p.checkedAt;
	const parts: string[] = [];
	if (p.windowSeconds) parts.push(`per ${windowLabel(p.windowSeconds)}`);
	parts.push(`checked ${formatRelative(at, now)}`);
	const left = p.remaining;
	return {
		text: left === undefined ? `${p.limit} allowed` : `${left} of ${p.limit} left`,
		sub: parts.join(' · '),
		tone: left !== undefined && left < p.limit * LOW_SHARE ? 'warn' : 'normal',
		title: formatDateTime(at)
	};
}
