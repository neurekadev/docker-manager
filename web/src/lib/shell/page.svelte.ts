// Page metadata for the shell (#22): the breadcrumbs in the top bar and
// the document title. Pages call usePage({ title, crumbs }) from their
// script; the shell prepends the selected environment when a page is
// environment-scoped. A layout and its pages may each register: the one
// registered last (the innermost) is shown, and when a page unmounts the
// layout's comes back (a stack's Logs tab, then a tab without its own).
import { onDestroy } from 'svelte';
import type { Crumb } from '$lib/ui/Breadcrumbs.svelte';

export interface PageMeta {
	title: string;
	/** Crumbs after the environment, the last being the page itself. */
	crumbs: Crumb[];
	/** Show the selected environment as the first crumb. */
	environmentScoped?: boolean;
}

const NO_PAGE: PageMeta = { title: 'Docker Manager', crumbs: [] };

interface Registration {
	get: () => PageMeta;
}

class PageState {
	// The list itself is not state: a page's removal runs in its teardown,
	// where Svelte reads state as it was before the navigation (the new
	// page's registration would be lost). `#changed` is replaced, never
	// read, on every change, so `current` follows.
	#registered: Registration[] = [];
	#changed = $state.raw({});

	get current(): PageMeta {
		void this.#changed;
		return this.#registered.at(-1)?.get() ?? NO_PAGE;
	}

	add(r: Registration): () => void {
		this.#registered = [...this.#registered, r];
		this.#changed = {};
		return () => {
			this.#registered = this.#registered.filter((x) => x !== r);
			this.#changed = {};
		};
	}
}

export const pageState = new PageState();

/**
 * Registers the page's title and crumbs for as long as the calling
 * component lives. Pass a function to keep them reactive.
 */
export function usePage(meta: PageMeta | (() => PageMeta)) {
	const get = typeof meta === 'function' ? meta : () => meta;
	// Registered while the component initialises, so a layout comes
	// before the pages inside it.
	onDestroy(pageState.add({ get }));
	$effect(() => {
		const m = pageState.current;
		document.title =
			m.title === 'Docker Manager' ? 'Docker Manager' : `${m.title} · Docker Manager`;
	});
}
