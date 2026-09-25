// Live-change pulse for resource tables (#22 motion, #23): rows whose
// signature (state, health, size, ...) changed since the last data are
// marked for one pulse. The first data marks nothing.

// The sets are replaced, never mutated, and the previous map is not
// rendered: plain collections are enough (no SvelteSet/SvelteMap).
/* eslint-disable svelte/prefer-svelte-reactivity */
export class ChangeTracker<T> {
	changed = $state<ReadonlySet<string>>(new Set());
	#prev: Map<string, string> | null = null;
	#timer: ReturnType<typeof setTimeout> | null = null;
	readonly #key: (row: T) => string;
	readonly #sig: (row: T) => string;

	constructor(key: (row: T) => string, sig: (row: T) => string) {
		this.#key = key;
		this.#sig = sig;
	}

	/** Feed each new list; returns the changed keys (also in `changed`). */
	update(rows: readonly T[]): ReadonlySet<string> {
		const next = new Map(rows.map((r) => [this.#key(r), this.#sig(r)]));
		const out = new Set<string>();
		if (this.#prev) {
			for (const [k, s] of next) if (this.#prev.get(k) !== s) out.add(k);
		}
		this.#prev = next;
		if (out.size) {
			this.changed = out;
			if (this.#timer) clearTimeout(this.#timer);
			this.#timer = setTimeout(() => (this.changed = new Set()), 1200);
		}
		return out;
	}
}
