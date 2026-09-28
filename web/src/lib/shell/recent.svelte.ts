// Recently visited pages for the ⌘K palette's "Recent" section (#22).
// UI state only: the paths are kept per browser tab in sessionStorage
// (docker-manager:recent-pages); page titles, which may name resources, stay
// in memory and are never stored. After a reload an entry without a known
// title shows only when it is a section page (the palette names it).
import type { StorageLike } from './environment.svelte';

export interface RecentPage {
	path: string;
	/** The page's title as shown, when known in this tab (memory only). */
	title?: string;
}

const KEY = 'docker-manager:recent-pages';

function tabStorage(): StorageLike | null {
	try {
		return typeof window === 'undefined' ? null : window.sessionStorage;
	} catch {
		return null;
	}
}

export class RecentPages {
	/** Newest first. */
	items = $state<RecentPage[]>([]);
	readonly max: number;
	#storage: StorageLike | null;

	constructor(storage: StorageLike | null = tabStorage(), max = 8) {
		this.#storage = storage;
		this.max = max;
		this.items = this.#load().map((path) => ({ path }));
	}

	#load(): string[] {
		try {
			const raw = this.#storage?.getItem(KEY);
			const v: unknown = raw ? JSON.parse(raw) : [];
			return Array.isArray(v)
				? v.filter((p): p is string => typeof p === 'string' && p.startsWith('/'))
				: [];
		} catch {
			return [];
		}
	}

	#save() {
		try {
			this.#storage?.setItem(KEY, JSON.stringify(this.items.map((i) => i.path)));
		} catch {
			// Not kept; the list still works in this tab.
		}
	}

	/** Records a visit to a path (moved to the front). */
	visit(path: string) {
		if (!path.startsWith('/') || this.items[0]?.path === path) return;
		const known = this.items.find((i) => i.path === path);
		this.items = [
			{ path, title: known?.title },
			...this.items.filter((i) => i.path !== path)
		].slice(0, this.max);
		this.#save();
	}

	/** Names a visited path with the title its page registered. */
	name(path: string, title: string) {
		if (!this.items.some((i) => i.path === path && i.title !== title)) return;
		this.items = this.items.map((i) => (i.path === path ? { ...i, title } : i));
	}
}

export const recentPages = new RecentPages();
