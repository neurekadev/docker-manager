// Start, Restart and Stop as one split button (LifecycleButton): the main
// part is Stop while anything runs (a partially running stack included)
// and Start while nothing does; the menu lists Start, Restart and Stop in
// that order, each only when the caller holds it, the ones that do not
// apply in the current state turned off (Start while all runs, Restart
// and Stop while nothing does). Pure; tested in lifecycle.spec.ts.

export type LifecycleVerb = 'start' | 'restart' | 'stop';

export interface LifecycleAction {
	run: () => void;
	/** Off in this state or for this object; the menu still lists it. */
	disabled?: boolean;
	/** Why it is off: the menu item's description, the main part's tooltip. */
	reason?: string;
}

/** The actions the caller holds; a missing one is hidden (#17: hide, don't disable). */
export type LifecycleActions = Partial<Record<LifecycleVerb, LifecycleAction>>;

export const LIFECYCLE_ORDER: readonly LifecycleVerb[] = ['start', 'restart', 'stop'];

export const LIFECYCLE_LABELS: Record<LifecycleVerb, string> = {
	start: 'Start',
	restart: 'Restart',
	stop: 'Stop'
};

/**
 * The main part's action: while anything runs Stop, else Restart, else a
 * Start that is on (the rest of a partially running stack); while nothing
 * runs Start. Undefined when there is none (the button is not shown).
 */
export function lifecycleMain(
	running: boolean,
	actions: LifecycleActions
): LifecycleVerb | undefined {
	if (!running) return actions.start ? 'start' : undefined;
	if (actions.stop) return 'stop';
	if (actions.restart) return 'restart';
	return actions.start && !actions.start.disabled ? 'start' : undefined;
}

export interface LifecycleEntry {
	verb: LifecycleVerb;
	label: string;
	disabled: boolean;
	description?: string;
}

/** The menu's entries: the held actions in the order Start, Restart, Stop. */
export function lifecycleEntries(actions: LifecycleActions, allOff = false): LifecycleEntry[] {
	return LIFECYCLE_ORDER.flatMap((verb) => {
		const a = actions[verb];
		if (!a) return [];
		return [
			{
				verb,
				label: LIFECYCLE_LABELS[verb],
				disabled: allOff || !!a.disabled,
				description: a.disabled ? a.reason : undefined
			}
		];
	});
}
