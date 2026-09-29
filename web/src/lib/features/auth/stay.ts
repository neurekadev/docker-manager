// "Stay signed in" on the sign-in page (#16): the browser remembers the
// last choice in localStorage under docker-manager:stay-signed-in ("1" or
// "0"; a preference, not a secret, no API data). Off until chosen. Storage
// may be missing (server rendering) or refuse access (private mode
// policies); then nothing is remembered. Pure (stay.spec.ts).
import type { StorageLike } from '$lib/shell/environment.svelte';

export const STAY_SIGNED_IN_KEY = 'docker-manager:stay-signed-in';

/** The browser's localStorage, or null where there is none or it is refused. */
export function browserStorage(): StorageLike | null {
	try {
		return typeof window === 'undefined' ? null : window.localStorage;
	} catch {
		return null;
	}
}

/** The remembered choice; false when nothing (readable) is stored. */
export function readStaySignedIn(storage: StorageLike | null = browserStorage()): boolean {
	try {
		return storage?.getItem(STAY_SIGNED_IN_KEY) === '1';
	} catch {
		return false;
	}
}

/** Remembers the choice for the next sign-in on this browser. */
export function rememberStaySignedIn(
	value: boolean,
	storage: StorageLike | null = browserStorage()
): void {
	try {
		storage?.setItem(STAY_SIGNED_IN_KEY, value ? '1' : '0');
	} catch {
		// Storage full or refused: the choice still applies to this sign-in.
	}
}
