// Per-item conflict resolution (#15, docs/internal/api/files.md "Conflicts and
// previews"). The preview lists the items whose name exists at the
// destination; the user decides per item (overwrite, skip, keep both),
// "Apply to all conflicts" is off by default, and the operation is sent as
// one request per decision group. Items without a conflict go in a `fail`
// group: if a name appears between the preview and the run, that item fails
// and is reported instead of overwriting anything.

export type ConflictChoice = 'overwrite' | 'skip' | 'keep_both';
export type ConflictPolicy = 'fail' | ConflictChoice;

export interface ConflictItem {
	/** Source path (copy/move), file name (upload) or archive member. */
	source: string;
	destination: string;
	existing: { name: string; type: string; size: number; modifiedAt: string };
	/** A copy into the entry's own folder: only "keep both" can do it. */
	self?: boolean;
}

export interface RequestGroup {
	conflict: ConflictPolicy;
	paths: string[];
}

/** No conflicts, nothing decided. */
export const NO_DECISIONS: ReadonlyMap<string, ConflictChoice> = new Map();

export interface GroupedRequests {
	groups: RequestGroup[];
	/** Conflicting items the user chose to skip (no request is sent). */
	skipped: string[];
}

/**
 * Splits `sources` into requests by decision. Throws when a conflicting
 * source has no decision (the dialog must finish first).
 */
export function groupRequests(
	sources: readonly string[],
	conflicts: readonly ConflictItem[],
	decisions: ReadonlyMap<string, ConflictChoice>
): GroupedRequests {
	const conflicting = new Set(conflicts.map((c) => c.source));
	const by: Record<ConflictPolicy, string[]> = {
		fail: [],
		overwrite: [],
		keep_both: [],
		skip: []
	};
	for (const s of sources) {
		if (!conflicting.has(s)) {
			by.fail.push(s);
			continue;
		}
		const d = decisions.get(s);
		if (!d) throw new Error(`no decision for ${s}`);
		by[d].push(s);
	}
	const groups: RequestGroup[] = [];
	for (const policy of ['fail', 'overwrite', 'keep_both'] as const) {
		if (by[policy].length) groups.push({ conflict: policy, paths: by[policy] });
	}
	return { groups, skipped: by.skip };
}

/**
 * The conflict dialog's queue: one conflict at a time, a decision moves to
 * the next; with applyToAll the decision also covers every remaining one.
 */
export class ConflictQueue {
	readonly items: readonly ConflictItem[];
	readonly decisions = new Map<string, ConflictChoice>();
	#index = 0;

	constructor(items: readonly ConflictItem[]) {
		this.items = items;
	}

	get current(): ConflictItem | null {
		return this.items[this.#index] ?? null;
	}

	/** 1-based position of the current conflict. */
	get position(): number {
		return Math.min(this.#index + 1, this.items.length);
	}

	get remaining(): number {
		return Math.max(0, this.items.length - this.#index);
	}

	get done(): boolean {
		return this.#index >= this.items.length;
	}

	decide(choice: ConflictChoice, applyToAll = false): void {
		if (this.done) return;
		const end = applyToAll ? this.items.length : this.#index + 1;
		for (let i = this.#index; i < end; i++) this.decisions.set(this.items[i].source, choice);
		this.#index = end;
	}
}

/** "Replace 2, skip 1, keep both for 1" summary of the decisions. */
export function decisionSummary(decisions: ReadonlyMap<string, ConflictChoice>): string {
	const n = { overwrite: 0, skip: 0, keep_both: 0 };
	for (const d of decisions.values()) n[d]++;
	const parts: string[] = [];
	if (n.overwrite) parts.push(`replace ${n.overwrite}`);
	if (n.skip) parts.push(`skip ${n.skip}`);
	if (n.keep_both) parts.push(`keep both for ${n.keep_both}`);
	if (!parts.length) return '';
	const s = parts.join(', ');
	return s[0].toUpperCase() + s.slice(1);
}
