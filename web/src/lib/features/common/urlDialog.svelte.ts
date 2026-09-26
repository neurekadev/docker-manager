// A dialog whose open state lives in a URL query parameter (#22): create
// and edit forms open as modals over their list or detail page, and links
// such as routes.updatePolicyNew() (`/updates?create=1`) open them from
// anywhere. Opening and closing replace the history entry, so Back leaves
// the page instead of toggling the dialog.
import { goto } from '$app/navigation';
import { page } from '$app/state';
import { SvelteURL } from 'svelte/reactivity';

export interface UrlDialog {
	open: boolean;
	/** Another query parameter of the URL (e.g. the preselected environment). */
	param(name: string): string | null;
}

/** `clear` lists parameters that belong to the dialog and go with it. */
export function urlDialog(name: string, clear: string[] = []): UrlDialog {
	return {
		get open() {
			return page.url.searchParams.has(name);
		},
		set open(v: boolean) {
			if (v === page.url.searchParams.has(name)) return;
			const url = new SvelteURL(page.url);
			if (v) url.searchParams.set(name, '1');
			else for (const n of [name, ...clear]) url.searchParams.delete(n);
			void goto(url, { replaceState: true, noScroll: true, keepFocus: true });
		},
		param(n: string) {
			return page.url.searchParams.get(n);
		}
	};
}
