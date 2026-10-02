// Which factor confirms it's you (#186), in the step-up dialog and at the
// second step of a sign-in alike: exactly one at a time, never two. A
// passkey comes first (its browser prompt opens at once), then the
// authenticator code, then the password for an account without a second
// factor (the manager accepts nothing else). With both a passkey and an
// authenticator app the user can switch to the code; the browser
// remembers that choice in localStorage under docker-manager:verify-with
// ("totp" or "passkey"; a preference, not a secret, no API data). Pure
// (verify.spec.ts).
import type { StorageLike } from '$lib/shell/environment.svelte';

export type VerifyMethod = 'passkey' | 'totp' | 'password';

export const VERIFY_WITH_KEY = 'docker-manager:verify-with';

/** What the account can confirm a step-up with, the default first. */
export function stepUpMethods(
	factors: { password: boolean; totp: boolean; passkeys: number },
	passkeysSupported: boolean
): VerifyMethod[] {
	const out: VerifyMethod[] = [];
	if (factors.passkeys > 0 && passkeysSupported) out.push('passkey');
	if (factors.totp) out.push('totp');
	if (!factors.totp && factors.passkeys === 0 && factors.password) out.push('password');
	return out;
}

/** What completes a pending sign-in (its factors), the default first. */
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
 * The method to start with: the remembered choice when the account has
 * it, otherwise the default (the first), or null when there is none.
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
	const pick = methods.find((m) => m === remembered);
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
