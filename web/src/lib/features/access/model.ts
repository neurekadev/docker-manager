// Accounts, groups, invitations and tokens (#16, #17, #31): presentation
// helpers. Pure (access.spec.ts).
import type { Account, Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui/Badge.svelte';

export function displayName(a: Pick<Account, 'displayName' | 'username'>): string {
	return a.displayName?.trim() || a.username;
}

/**
 * The second line under a name: the username when a display name is set
 * and differs from it (never the same word twice), else the email.
 */
export function secondaryName(
	a: Pick<Account, 'displayName' | 'username' | 'email'>
): string | undefined {
	const name = a.displayName?.trim();
	if (name && name.toLowerCase() !== a.username.toLowerCase()) return a.username;
	return a.email || undefined;
}

/** "Password, authenticator app and 2 passkeys" (what the account signs in with). */
export function factorsText(f: Account['factors']): string {
	const parts: string[] = [];
	if (f.password) parts.push('password');
	if (f.totp) parts.push('authenticator app');
	if (f.passkeys) parts.push(`${f.passkeys} ${f.passkeys === 1 ? 'passkey' : 'passkeys'}`);
	if (!parts.length) return 'None';
	const text =
		parts.length === 1 ? parts[0] : `${parts.slice(0, -1).join(', ')} and ${parts.at(-1)}`;
	return text[0].toUpperCase() + text.slice(1);
}

/**
 * The members of a group as its page lists them: every account in it
 * except the owner, whose access never comes from a group. The group
 * list counts the same way (the API's memberCount includes the owner).
 */
export function groupMembers<T extends Pick<Account, 'groupId' | 'owner'>>(
	users: readonly T[] | undefined,
	groupId: string
): T[] {
	return (users ?? []).filter((u) => u.groupId === groupId && !u.owner);
}

/** "1 member", "2 members". */
export function membersText(n: number): string {
	return `${n} ${n === 1 ? 'member' : 'members'}`;
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

/**
 * Accounts that can be added to a group: everyone in another group except
 * the owner, matching the search (name, username or email), by name.
 */
export function memberCandidates<
	T extends Pick<Account, 'groupId' | 'owner' | 'displayName' | 'username' | 'email'>
>(users: readonly T[] | undefined, groupId: string, search = ''): T[] {
	const q = search.trim().toLowerCase();
	return (users ?? [])
		.filter((u) => u.groupId !== groupId && !u.owner)
		.filter(
			(u) =>
				!q || [u.displayName, u.username, u.email].some((t) => t?.toLowerCase().includes(q))
		)
		.sort((a, b) => displayName(a).localeCompare(displayName(b)));
}
