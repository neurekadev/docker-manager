// Creating an API token moved with my tokens into the personal Profile:
// the old address (bookmarks, links in older docs) opens the form there.
import { redirect } from '@sveltejs/kit';
import { routes } from '$lib/routes';

export function load({ url }: { url: URL }) {
	redirect(307, `${routes.apiTokenNew()}${url.search}`);
}
