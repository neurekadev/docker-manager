// My API tokens moved out of Settings into the personal Profile: the old
// address (bookmarks, links in older docs) opens them there. Every user's
// tokens stay in Settings (/settings/tokens/all).
import { redirect } from '@sveltejs/kit';
import { routes } from '$lib/routes';

export function load({ url }: { url: URL }) {
	redirect(307, `${routes.apiTokens()}${url.search}`);
}
