// Sign-in policy (#16) wording. Pure (settings.spec.ts).
import type { SecuritySettings } from './queries';

export type RequiredFactors = SecuritySettings['requiredFactors'];

export const FACTOR_POLICY: Record<RequiredFactors, string> = {
	none: 'Password or passkey',
	totp: 'Password and authenticator app',
	passkey: 'Passkey',
	either: 'Authenticator app or passkey',
	both: 'Authenticator app and passkey'
};

/** What choosing a factor policy means for the people who sign in. */
export const FACTOR_DETAIL: Record<RequiredFactors, string> = {
	none: 'A password (plus TOTP for accounts that enabled it) or a passkey.',
	totp: 'Every password sign-in also needs a 6-digit code from an authenticator app.',
	passkey: 'Everyone signs in with a passkey (with user verification).',
	either: 'A password plus an authenticator code, or a passkey.',
	both: 'A password, an authenticator code and a passkey.'
};

/**
 * Consequences of changing the required factors (#16: staged enrollment,
 * never a lock-out). Empty when nothing changes.
 */
export function factorChangeConsequences(
	from: RequiredFactors,
	to: RequiredFactors,
	graceHours: number
): string[] {
	if (from === to) return [];
	return [
		'Every other session ends at once; everyone signs in again.',
		`Accounts without the required factors get a limited session to add them, for ${graceHours} hours; after that they can only sign in to enroll with the owner's help.`,
		'You keep your session. If you lack the new factors, it is limited to adding them (the owner has no deadline).',
		'API tokens are not affected: they never satisfy sign-in factors.'
	];
}

/** Diff of the editable fields for the confirmation dialog. */
export function settingsChanges(
	before: SecuritySettings,
	after: Partial<SecuritySettings>
): string[] {
	const out: string[] = [];
	const add = (label: string, a: unknown, b: unknown) => {
		if (b !== undefined && a !== b) out.push(`${label}: ${fmt(a)} → ${fmt(b)}`);
	};
	add('Strict passwords', before.strictPasswords, after.strictPasswords);
	add('Minimum password length', before.minPasswordLength, after.minPasswordLength);
	add(
		'Required sign-in',
		FACTOR_POLICY[before.requiredFactors],
		after.requiredFactors && FACTOR_POLICY[after.requiredFactors]
	);
	add('Enrollment grace (hours)', before.enrollmentGraceHours, after.enrollmentGraceHours);
	add('Invitation lifetime (hours)', before.invitationTtlHours, after.invitationTtlHours);
	add(
		'Password reset lifetime (hours)',
		before.passwordResetTtlHours,
		after.passwordResetTtlHours
	);
	add('API tokens', before.apiTokensEnabled, after.apiTokensEnabled);
	add(
		'Longest token lifetime (days)',
		before.apiTokenMaxLifetimeDays,
		after.apiTokenMaxLifetimeDays
	);
	add('Tokens without expiry', before.apiTokensNonExpiring, after.apiTokensNonExpiring);
	return out;
}

function fmt(v: unknown): string {
	if (v === true) return 'on';
	if (v === false) return 'off';
	return String(v);
}
