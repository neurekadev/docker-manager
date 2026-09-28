// The backup policies are the main table of the Backups overview (#10): the
// old list address (bookmarks, job links, ?create=1) opens the overview.
import { redirect } from '@sveltejs/kit';
import { routes } from '$lib/routes';

export function load({ url }: { url: URL }) {
	redirect(307, `${routes.backups()}${url.search}`);
}
