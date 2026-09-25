// Session lifecycle in the browser (#16, #22): sign-out, session expiry
// and what is cleared. The session cookie is HttpOnly; the UI only knows
// the Session returned by GET /auth/session (queryKeys.session).
//
// Rules:
// - Any API answer 401 while the UI believes it is signed in means the
//   session ended (idle/absolute expiry, revoked, disabled account): drop
//   every cached API response and go to sign-in with reason=expired and the
//   current path as next.
// - Sign-out deletes the session server-side, then drops every cached API
//   response and the per-tab shell state (environment choice, notices).
import type { QueryClient } from '@tanstack/svelte-query';
import { api, type ApiClient, type Session } from '$lib/api/client';
import { queryKeys } from '$lib/api/queries';
import { routes } from '$lib/routes';

/** Drops every cached API response except the public setup/health data. */
export function dropPrivateData(qc: QueryClient) {
	qc.cancelQueries();
	qc.removeQueries({
		predicate: (q) => {
			const root = q.queryKey[0];
			return root !== 'health' && root !== 'setup';
		}
	});
}

export interface SessionEnvironment {
	queryClient: QueryClient;
	/** Navigate (SvelteKit goto). */
	navigate: (url: string) => void | Promise<void>;
	/** The current in-app path with query (for ?next=). */
	currentPath: () => string;
	/** Per-tab state to reset on sign-out/expiry. */
	resetShell?: () => void;
}

/** Handles a 401 from any request (QueryClient hook). */
export function handleUnauthenticated(env: SessionEnvironment) {
	const qc = env.queryClient;
	const session = qc.getQueryData<Session | null>(queryKeys.session);
	if (!session) return; // already signed out: the page shows sign-in
	const path = env.currentPath();
	dropPrivateData(qc);
	qc.setQueryData(queryKeys.session, null);
	env.resetShell?.();
	void env.navigate(routes.signIn(path, 'expired'));
}

/** Signs out (DELETE /auth/session) and clears everything private. */
export async function signOut(env: SessionEnvironment, client: ApiClient = api) {
	try {
		await client.DELETE('/api/v1/auth/session');
	} catch {
		// Offline: the cookie stays until it expires, but this tab forgets it.
	}
	dropPrivateData(env.queryClient);
	env.queryClient.setQueryData(queryKeys.session, null);
	env.resetShell?.();
	await env.navigate(routes.signIn(undefined, 'signed-out'));
}

/**
 * Milliseconds until the session should be re-checked (its idle or
 * absolute expiry, whichever is first, plus a second), or null.
 */
export function msUntilExpiryCheck(s: Session | null | undefined, now = Date.now()): number | null {
	if (!s) return null;
	const times = [s.expiresAt, s.idleExpiresAt].filter(Boolean).map((t) => new Date(t!).getTime());
	if (!times.length) return null;
	return Math.max(0, Math.min(...times) - now + 1000);
}
