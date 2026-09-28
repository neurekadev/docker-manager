// Profile and security moved out of Settings into the personal Profile:
// the old address (bookmarks, links in older docs) opens it there.
import { redirect } from '@sveltejs/kit';
import { routes } from '$lib/routes';

export function load({ url }: { url: URL }) {
	redirect(307, `${routes.profile()}${url.search}`);
}
