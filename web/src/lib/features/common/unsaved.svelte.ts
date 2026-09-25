// Critical work (#23) held by a component: unsaved form edits, a Recovery
// Key not confirmed yet, a restore or an import in progress. The PWA update
// prompt does not reload the page while any is registered. Call from a
// component's script; the registration follows `active()` and is released
// when it turns false or the component unmounts.
import { untrack } from 'svelte';
import { criticalWork, type CriticalKind } from '$lib/live';

export function useCriticalWork(kind: CriticalKind, label: () => string, active: () => boolean) {
	$effect(() => {
		if (!active()) return;
		const text = label();
		// register() reads the registry: keep it out of this effect's dependencies.
		return untrack(() => criticalWork.register(kind, text));
	});
}

/** Unsaved edits of a form or editor. */
export function useUnsaved(label: () => string, dirty: () => boolean) {
	useCriticalWork('unsaved-edit', label, dirty);
}
