// The search text and filters of one list (containers, images, volumes,
// networks, stacks), kept per list and per browser tab in sessionStorage
// under docker-manager:list-filters:<list>, so leaving a section and coming
// back restores them. Only UI state is stored (never API data); the stored
// value is parsed defensively (filters.ts). Without sessionStorage (SSR,
// tests, blocked storage) the state lives in memory only.
import {
	emptyFilterState,
	parseFilterState,
	serializeFilterState,
	type ListFilterState
} from './filters';

export const LIST_FILTERS_PREFIX = 'docker-manager:list-filters:';

type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

function sessionStore(): Store | null {
	try {
		return typeof sessionStorage === 'undefined' ? null : sessionStorage;
	} catch {
		return null; // access denied (storage disabled)
	}
}

export class ListFilters {
	readonly key: string;
	#storage: Store | null;
	#state = $state<ListFilterState>(emptyFilterState());

	/** `list`: the list's name ("containers"); `storage`: sessionStorage unless given. */
	constructor(list: string, storage: Store | null = sessionStore()) {
		this.key = LIST_FILTERS_PREFIX + list;
		this.#storage = storage;
		let raw: string | null;
		try {
			raw = storage?.getItem(this.key) ?? null;
		} catch {
			raw = null; // access denied
		}
		this.#state = parseFilterState(raw);
	}

	/** The search text. */
	get q(): string {
		return this.#state.q;
	}

	set q(v: string) {
		this.#state.q = v;
		this.#save();
	}

	/** The whole state (search and filter values). */
	get state(): ListFilterState {
		return this.#state;
	}

	get(id: string): string {
		return this.#state.values[id] ?? '';
	}

	set(id: string, value: string) {
		if (value) this.#state.values[id] = value;
		else delete this.#state.values[id];
		this.#save();
	}

	/** Resets the search and every filter. */
	clear() {
		this.#state = emptyFilterState();
		this.#save();
	}

	#save() {
		if (!this.#storage) return;
		const raw = serializeFilterState(this.#state);
		try {
			if (raw === null) this.#storage.removeItem(this.key);
			else this.#storage.setItem(this.key, raw);
		} catch {
			// Quota or access errors: the filters still work for this visit.
		}
	}
}
