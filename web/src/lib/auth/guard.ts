// Route guard (#16, #22): where a visitor belongs given the session and
// the setup status. Pure, so every rule is unit-tested; the (app) layout
// and the public pages apply it.
import type { Session } from '$lib/api/client';
import { routes, safeNext } from '$lib/routes';

export type PublicPage = 'setup' | 'sign-in' | 'enroll' | 'invitation' | 'password-reset';

/**
 * Where an app page (anything behind sign-in) must send the visitor, or
 * null to render it. `setupComplete` undefined means "not known yet".
 */
export function appDestination(
	path: string,
	session: Session | null,
	setupComplete: boolean | undefined
): string | null {
	if (session?.state === 'authenticated') return null;
	if (session?.state === 'enrollment_required') return routes.enroll();
	if (session?.state === 'second_factor_required') return routes.signIn(path);
	if (setupComplete === false) return routes.setup();
	return routes.signIn(path);
}

/**
 * Where a public page must send the visitor instead, or null to render it.
 * Signed-in users skip sign-in and setup; setup is only reachable while
 * the owner does not exist; enrollment needs an enrollment session.
 */
export function publicDestination(
	page: PublicPage,
	session: Session | null,
	setupComplete: boolean | undefined,
	next?: string | null
): string | null {
	const state = session?.state;
	switch (page) {
		case 'setup':
			if (state === 'authenticated') return safeNext(next);
			if (setupComplete === true) return routes.signIn();
			return null;
		case 'sign-in':
			if (state === 'authenticated') return safeNext(next);
			if (state === 'enrollment_required') return routes.enroll();
			if (setupComplete === false) return routes.setup();
			return null;
		case 'enroll':
			if (state === 'authenticated') return safeNext(next);
			if (state !== 'enrollment_required') return routes.signIn();
			return null;
		case 'invitation':
		case 'password-reset':
			// Redeeming works signed out; a signed-in user may still open the
			// link (e.g. on a shared machine) and is not redirected.
			return null;
	}
}
