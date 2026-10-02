// Which factor confirms it's you (#186), in the step-up dialog and at the
// second step of a sign-in alike: exactly one at a time, never two, in
// the order passkey (its browser prompt opens at once), authenticator
// code, password. The user can switch to any other factor the account
// has; the password is a fallback for every account with one. A switch
// between passkey and code is remembered in localStorage under
// docker-manager:verify-with ("totp" or "passkey"; a preference, not a
// secret, no API data); the password is never the remembered default.
// Pure (verify.spec.ts).
import type { StorageLike } from '$lib/shell/environment.svelte';

export type VerifyMethod = 'passkey' | 'totp' | 'password';

export const VERIFY_WITH_KEY = 'docker-manager:verify-with';

/** What the account can confirm a step-up with, in priority order. */
export function stepUpMethods(
	factors: { password: boolean; totp: boolean; passkeys: number },
	passkeysSupported: boolean
): VerifyMethod[] {
	const out: VerifyMethod[] = [];
	if (factors.passkeys > 0 && passkeysSupported) out.push('passkey');
	if (factors.totp) out.push('totp');
	if (factors.password) out.push('password');
	return out;
}

/** What completes a pending sign-in (its factors), in priority order. */
export function secondFactorMethods(factors: string[], passkeysSupported: boolean): VerifyMethod[] {
	const out: VerifyMethod[] = [];
	if (factors.includes('passkey') && passkeysSupported) out.push('passkey');
	if (factors.includes('totp')) out.push('totp');
	return out;
}

function browserStorage(): StorageLike | null {
	try {
		return typeof window === 'undefined' ? null : window.localStorage;
	} catch {
		return null;
	}
}

/**
 * The method to start with: the remembered choice (passkey or code) when
 * the account has it, otherwise the first by priority, or null.
 */
export function initialMethod(
	methods: VerifyMethod[],
	storage: StorageLike | null = browserStorage()
): VerifyMethod | null {
	let remembered: string | null = null;
	try {
		remembered = storage?.getItem(VERIFY_WITH_KEY) ?? null;
	} catch {
		// Refused: the default applies.
	}
	const pick = methods.find((m) => m !== 'password' && m === remembered);
	return pick ?? methods[0] ?? null;
}

/** Remembers a switch between passkey and code for the next check. */
export function rememberMethod(
	method: VerifyMethod,
	storage: StorageLike | null = browserStorage()
): void {
	if (method === 'password') return;
	try {
		storage?.setItem(VERIFY_WITH_KEY, method);
	} catch {
		// Storage full or refused: the switch still applies now.
	}
}
