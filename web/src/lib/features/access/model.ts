// Accounts, groups, invitations and tokens (#16, #17, #31): presentation
// helpers. Pure (access.spec.ts).
import type { Account, Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui/Badge.svelte';

export function displayName(a: Pick<Account, 'displayName' | 'username'>): string {
	return a.displayName?.trim() || a.username;
}

/** "Password, TOTP, 2 passkeys" (what the account signs in with). */
export function factorsText(f: Account['factors']): string {
	const parts: string[] = [];
	if (f.password) parts.push('Password');
	if (f.totp) parts.push('TOTP');
	if (f.passkeys) parts.push(`${f.passkeys} ${f.passkeys === 1 ? 'passkey' : 'passkeys'}`);
	return parts.length ? parts.join(', ') : 'None';
}

export function accountStatus(a: Pick<Account, 'status' | 'owner' | 'enrollmentDeadline'>): {
	tone: BadgeTone;
	label: string;
} {
	if (a.status === 'disabled') return { tone: 'neutral', label: 'Disabled' };
	if (a.enrollmentDeadline) return { tone: 'warn', label: 'Enrolling factors' };
	return { tone: 'ok', label: 'Active' };
}

type Invitation = Schema<'Invitation'>;

const INVITATION: Record<Invitation['status'], { tone: BadgeTone; label: string }> = {
	pending: { tone: 'info', label: 'Pending' },
	redeemed: { tone: 'ok', label: 'Redeemed' },
	expired: { tone: 'neutral', label: 'Expired' },
	revoked: { tone: 'neutral', label: 'Revoked' }
};

export function invitationStatus(s: Invitation['status']) {
	return INVITATION[s] ?? { tone: 'neutral' as BadgeTone, label: s };
}

type Token = Schema<'APIToken'>;

const TOKEN: Record<Token['status'], { tone: BadgeTone; label: string }> = {
	active: { tone: 'ok', label: 'Active' },
	expired: { tone: 'neutral', label: 'Expired' },
	revoked: { tone: 'neutral', label: 'Revoked' }
};

export function tokenStatus(s: Token['status']) {
	return TOKEN[s] ?? { tone: 'neutral' as BadgeTone, label: s };
}

export const REVOKED_REASON: Record<NonNullable<Token['revokedReason']>, string> = {
	user: 'Revoked by its user',
	owner: 'Revoked by the owner',
	user_disabled: 'Revoked: the account was disabled',
	credential_reset: 'Revoked with a password or factor reset',
	restore: 'Revoked by a manager restore'
};

/** Default expiry of a new token: now + days, as an RFC 3339 instant. */
export function expiryFromDays(days: number, now: Date = new Date()): string {
	return new Date(now.getTime() + days * 86400_000).toISOString();
}

/** Whether the requested lifetime exceeds the instance maximum. */
export function exceedsMaxLifetime(days: number, maxDays: number | undefined): boolean {
	return maxDays !== undefined && days > maxDays;
}
