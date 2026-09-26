// Page metadata for the shell (#22): the breadcrumbs in the top bar and
// the document title. Pages call usePage({ title, crumbs }) from their
// script; the shell prepends the selected environment when a page is
// environment-scoped. Cleared automatically when the page unmounts.
import type { Crumb } from '$lib/ui/Breadcrumbs.svelte';

export interface PageMeta {
	title: string;
	/** Crumbs after the environment, the last being the page itself. */
	crumbs: Crumb[];
	/** Show the selected environment as the first crumb. */
	environmentScoped?: boolean;
}

class PageState {
	current = $state<PageMeta>({ title: 'Docker Manager', crumbs: [] });
}

export const pageState = new PageState();

/**
 * Registers the page's title and crumbs for as long as the calling
 * component lives. Pass a function to keep them reactive.
 */
export function usePage(meta: PageMeta | (() => PageMeta)) {
	const get = typeof meta === 'function' ? meta : () => meta;
	$effect(() => {
		const m = get();
		pageState.current = m;
		document.title =
			m.title === 'Docker Manager' ? 'Docker Manager' : `${m.title} · Docker Manager`;
	});
}
