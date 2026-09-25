// Critical work registry (#23): work that a page reload would destroy.
//
// Views register what must not be interrupted and release it when done:
//
//   const release = criticalWork.register('unsaved-edit', 'compose.yaml');
//   ... on save or discard: release();
//
// The PWA update prompt consults it: while anything is registered the new
// build is not applied (the prompt says what is still open), so an unsaved
// file edit, a live terminal, an in-progress restore or upload is never
// reloaded away. Release is idempotent; register again for new work.

import { untrack } from 'svelte';

export type CriticalKind = 'unsaved-edit' | 'terminal' | 'restore' | 'upload' | 'other';

export interface CriticalItem {
	id: number;
	kind: CriticalKind;
	/** Short user-facing description, e.g. a file name or container name. */
	label: string;
}

export class CriticalWork {
	#next = 1;
	items = $state<CriticalItem[]>([]);
	readonly active: boolean = $derived(this.items.length > 0);

	/** Registers work; returns its release function. */
	register(kind: CriticalKind, label: string): () => void {
		const id = this.#next++;
		// Untracked: views register from $effect, which must not come to
		// depend on the registry it writes (effect_update_depth_exceeded).
		untrack(() => (this.items = [...this.items, { id, kind, label }]));
		let released = false;
		return () => {
			if (released) return;
			released = true;
			untrack(() => (this.items = this.items.filter((i) => i.id !== id)));
		};
	}
}

export const criticalWork = new CriticalWork();
