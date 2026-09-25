// Shared plumbing of the public auth pages (#16, #22): the redirect guard
// and what happens after the manager answers with a Session.
import { createQuery, useQueryClient } from '@tanstack/svelte-query';
import { goto } from '$app/navigation';
import { page } from '$app/state';
import type { Session } from '$lib/api/client';
import { queryKeys, sessionQuery, setupStatusQuery } from '$lib/api/queries';
import { liveClient } from '$lib/live';
import { routes, safeNext } from '$lib/routes';
import { publicDestination, type PublicPage } from './guard';

/**
 * Loads the session and setup status for a public page and redirects away
 * when the page does not apply (e.g. sign-in while signed in). `ready` is
 * true once the page may render its form.
 */
export function usePublicPage(which: PublicPage) {
	const session = createQuery(() => sessionQuery());
	const setup = createQuery(() => setupStatusQuery());
	const qc = useQueryClient();
	let redirecting = $state(false);

	$effect(() => {
		if (session.isPending || setup.isPending) return;
		const to = publicDestination(
			which,
			session.data ?? null,
			setup.data?.setupComplete,
			page.url.searchParams.get('next')
		);
		if (to && to !== page.url.pathname + page.url.search) {
			redirecting = true;
			void goto(to, { replaceState: true });
		}
	});

	return {
		get session() {
			return session;
		},
		get setup() {
			return setup;
		},
		get ready() {
			return !session.isPending && !setup.isPending && !redirecting;
		},
		/** Records a Session answered by the manager and moves on. */
		async proceed(s: Session) {
			qc.setQueryData(queryKeys.session, s);
			// The account changed: nothing cached from before may leak into it.
			qc.removeQueries({
				predicate: (q) => !['session', 'setup', 'health'].includes(String(q.queryKey[0]))
			});
			if (s.state === 'authenticated') {
				// The live stream stopped at 401 while signed out (#23).
				liveClient()?.reconnectNow();
				redirecting = true;
				await goto(safeNext(page.url.searchParams.get('next')), { replaceState: true });
			} else if (s.state === 'enrollment_required') {
				redirecting = true;
				await goto(routes.enroll(), { replaceState: true });
			}
		}
	};
}
