// Sign-in policy (#16) and instance settings (#4) wording. Pure
// (settings.spec.ts).
import { formatBytes } from '$lib/ui/format';
import type { InstanceSettings, SecuritySettings } from './queries';

/** Longest display name of the instance (the server's limit). */
export const MAX_INSTANCE_NAME = 64;

/**
 * Checks a display name like the server does (surrounding white space is
 * removed; 1-64 characters; no control characters). Returns the problem to
 * show, or null.
 */
export function instanceNameProblem(name: string): string | null {
	const n = name.trim();
	if (n === '') return 'Enter a name.';
	if ([...n].length > MAX_INSTANCE_NAME) return `Use at most ${MAX_INSTANCE_NAME} characters.`;
	for (const ch of n) {
		const c = ch.codePointAt(0) ?? 0;
		if (c < 0x20 || (c >= 0x7f && c <= 0x9f))
			return 'Remove line breaks and control characters.';
	}
	return null;
}

/** The read-only deployment configuration as label/value facts. */
export function deploymentFacts(
	s: InstanceSettings
): { label: string; value: string; mono?: boolean }[] {
	const d = s.deployment;
	const proxies = d.trustedProxyCount;
	return [
		{ label: 'Public URL', value: d.publicUrl || 'Not set', mono: !!d.publicUrl },
		{
			label: 'Mode',
			value: d.localDevelopment
				? 'Local development over plain HTTP'
				: 'HTTPS behind a reverse proxy'
		},
		{
			label: 'Trusted proxies',
			value:
				proxies === 0
					? 'None: forwarded headers are ignored'
					: `${proxies} address ${proxies === 1 ? 'range' : 'ranges'}`
		},
		{ label: 'Stream heartbeat', value: `Every ${d.streamHeartbeatSeconds} s` },
		{ label: 'Largest upload', value: formatBytes(d.filesMaxUploadBytes) },
		{ label: 'Largest file to edit', value: formatBytes(d.filesMaxEditBytes) },
		{ label: 'Largest download or archive', value: formatBytes(d.filesMaxDownloadBytes) },
		{
			label: 'Largest extraction',
			value: `${formatBytes(d.filesMaxExtractBytes)}, at most ${d.filesMaxExtractRatio}× the archive, ${d.filesMaxArchiveEntries.toLocaleString('en')} entries`
		},
		{ label: 'Metrics endpoint', value: d.metricsEndpoint ? 'On' : 'Off' }
	];
}

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
	none: 'A password (plus a code from an authenticator app for accounts that set one up) or a passkey.',
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

/**
 * Consequences of turning "Stay signed in" off (#16): devices that stayed
 * signed in fall back to the normal session limits. Empty otherwise.
 */
export function staySignedInConsequences(from: boolean, to: boolean): string[] {
	if (!from || to) return [];
	return [
		'The sign-in page stops offering Stay signed in.',
		'Devices that stayed signed in move back to the normal limits; those already past them are signed out.'
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
	add('Stay signed in', before.allowStaySignedIn, after.allowStaySignedIn);
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
