// Digest-driven updates (#20): presentation of policies and candidates.
// Pure functions (tested in model.spec.ts); the API decides eligibility.
import type { Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui/Badge.svelte';

export type UpdatePolicy = Schema<'UpdatePolicy'>;
export type UpdateCandidate = Schema<'UpdateCandidate'>;
export type UpdatePreview = Schema<'UpdatePreview'>;
export type UpdateWindow = Schema<'UpdateWindow'>;
export type CandidateStatus = UpdateCandidate['status'];

export interface Presentation {
	tone: BadgeTone;
	label: string;
}

const STATUS: Record<CandidateStatus, Presentation> = {
	update_available: { tone: 'warn', label: 'Update available' },
	up_to_date: { tone: 'ok', label: 'Up to date' },
	unchecked: { tone: 'neutral', label: 'Not checked yet' },
	ineligible: { tone: 'neutral', label: 'Not eligible' },
	quarantined: { tone: 'danger', label: 'Quarantined' },
	check_failed: { tone: 'danger', label: 'Check failed' },
	run_failed: { tone: 'danger', label: 'Update failed' }
};

export function candidateStatus(s: CandidateStatus): Presentation {
	return STATUS[s] ?? { tone: 'neutral', label: s };
}

/** Why a candidate is not eligible, in the words of the user's definition. */
export const REASON_LABELS: Record<NonNullable<UpdateCandidate['reason']>, string> = {
	build_only: 'Built from source (no image to follow)',
	digest_pinned: 'Pinned by @sha256 digest',
	untagged: 'No tag in the image reference',
	pull_policy_conflict: 'Its pull_policy prevents remote updates',
	invalid_reference: 'The image reference is not valid',
	not_deployed: 'Not deployed yet',
	no_applied_digest: 'The applied digest is unknown (deploy it once)',
	excluded: 'Excluded by this policy',
	protected: "Docker Manager's own container",
	no_recreate_spec: 'No saved specification to recreate it',
	stack_managed: 'Part of a stack (use the stack policy)'
};

export function reasonLabel(c: UpdateCandidate): string | null {
	if (!c.reason) return null;
	return REASON_LABELS[c.reason] ?? c.reasonMessage ?? c.reason;
}

/** Registry error classes of a failed check, in plain words. */
export const CHECK_ERRORS: Record<string, string> = {
	unauthorized: 'The registry refused the credentials (401).',
	forbidden: 'The registry denied access to this repository (403).',
	rate_limited: 'The registry rate-limited the check (429).',
	registry_unavailable: 'The registry could not be reached.',
	not_found: 'The tag does not exist in the registry.',
	platform_not_found: "The registry has no image for this host's platform.",
	ambiguous_registry_connection: 'More than one registry connection matches this image.',
	registry_connection_revoked: 'The registry connection was revoked.'
};

export function checkErrorText(c: UpdateCandidate): string | null {
	if (!c.errorClass) return null;
	return CHECK_ERRORS[c.errorClass] ?? c.errorMessage ?? c.errorClass;
}

/** One-line summary of a policy's candidates for lists. */
export function summaryText(p: UpdatePolicy): { text: string; tone: BadgeTone } {
	const s = p.summary;
	if (!s) return { text: 'Not checked yet', tone: 'neutral' };
	if (s.quarantined > 0) return { text: `${s.quarantined} quarantined`, tone: 'danger' };
	if (s.failed > 0) return { text: `${s.failed} failed`, tone: 'danger' };
	if (s.available > 0)
		return {
			text: `${s.available} ${s.available === 1 ? 'update' : 'updates'} available`,
			tone: 'warn'
		};
	if (!s.lastCheckAt) return { text: 'Not checked yet', tone: 'neutral' };
	if (s.upToDate > 0) return { text: 'Up to date', tone: 'ok' };
	return { text: 'Nothing eligible', tone: 'neutral' };
}

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

/** "Mon–Fri 01:00–05:00" style text of an update window (null: any time). */
export function windowText(w: UpdateWindow | undefined | null): string {
	if (!w) return 'Any time';
	const days = w.days && w.days.length > 0 && w.days.length < 7 ? daysText(w.days) : 'Every day';
	return `${days}, ${w.start}–${w.end}`;
}

export function daysText(days: number[]): string {
	const sorted = [...new Set(days)].sort((a, b) => a - b);
	// Collapse consecutive runs: [1,2,3,4,5] → Mon–Fri.
	const parts: string[] = [];
	let i = 0;
	while (i < sorted.length) {
		let j = i;
		while (j + 1 < sorted.length && sorted[j + 1] === sorted[j] + 1) j++;
		parts.push(
			j - i >= 2
				? `${DAYS[sorted[i]]}–${DAYS[sorted[j]]}`
				: sorted
						.slice(i, j + 1)
						.map((d) => DAYS[d])
						.join(', ')
		);
		i = j + 1;
	}
	return parts.join(', ');
}

export const DAY_OPTIONS = DAYS.map((label, value) => ({ value, label }));

/** HH:MM (24 h) validation of window bounds. */
export function isClock(s: string): boolean {
	return /^([01]\d|2[0-3]):[0-5]\d$/.test(s);
}

/** Candidates a run would apply (the preview's default selection). */
export function runnable(candidates: UpdateCandidate[]): UpdateCandidate[] {
	return candidates.filter((c) => c.eligible && c.status === 'update_available');
}

/**
 * Manual recovery of a quarantined or failed candidate (#20: no automatic
 * rollback). The manager's guidance wins; this is the fallback text.
 */
export function recoveryText(c: UpdateCandidate): string {
	if (c.guidance) return c.guidance;
	const pin = c.previousDigest ?? c.currentDigest;
	const repo = c.repository ?? c.reference.replace(/[:@].*$/, '');
	return pin
		? `Pin the previous image in your own Compose file, e.g. ${repo}@${pin}, then deploy the stack. Docker Manager never edits your files.`
		: 'Pin a known-good digest (image@sha256:…) in your own Compose file and deploy the stack. Docker Manager never edits your files.';
}
