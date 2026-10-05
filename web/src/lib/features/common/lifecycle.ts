// Start, Restart and Stop as one split button (LifecycleButton): the main
// part is Stop while anything runs (a partially running stack included)
// and Start while nothing does; the menu lists Start, Restart, Stop and a
// stack's Take Down in that order, each only when the caller holds it, the
// ones that do not apply in the current state turned off (Start while all
// runs, Restart and Stop while nothing does). Take Down is the main part
// only when it is the one action that applies. Pure; tested in
// lifecycle.spec.ts.

export type LifecycleVerb = 'start' | 'restart' | 'stop' | 'down';

export interface LifecycleAction {
	run: () => void;
	/** Off in this state or for this object; the menu still lists it. */
	disabled?: boolean;
	/** Why it is off: the main part's tooltip. */
	reason?: string;
}

/** The actions the caller holds; a missing one is hidden (#17: hide, don't disable). */
export type LifecycleActions = Partial<Record<LifecycleVerb, LifecycleAction>>;

export const LIFECYCLE_ORDER: readonly LifecycleVerb[] = ['start', 'restart', 'stop', 'down'];

export const LIFECYCLE_LABELS: Record<LifecycleVerb, string> = {
	start: 'Start',
	restart: 'Restart',
	stop: 'Stop',
	down: 'Take Down'
};

/**
 * The main part's action: while anything runs Stop, else Restart, else a
 * Start that is on (the rest of a partially running stack); while nothing
 * runs Start. Without those, a Take Down that is on. Undefined when there
 * is none (the button is not shown).
 */
export function lifecycleMain(
	running: boolean,
	actions: LifecycleActions
): LifecycleVerb | undefined {
	const down = actions.down && !actions.down.disabled ? 'down' : undefined;
	if (!running) return actions.start ? 'start' : down;
	if (actions.stop) return 'stop';
	if (actions.restart) return 'restart';
	return actions.start && !actions.start.disabled ? 'start' : down;
}

export interface LifecycleEntry {
	verb: LifecycleVerb;
	label: string;
	disabled: boolean;
}

/** The menu's entries: the held actions in the order Start, Restart, Stop, Take Down. */
export function lifecycleEntries(actions: LifecycleActions, allOff = false): LifecycleEntry[] {
	return LIFECYCLE_ORDER.flatMap((verb) => {
		const a = actions[verb];
		if (!a) return [];
		return [
			{
				verb,
				label: LIFECYCLE_LABELS[verb],
				disabled: allOff || !!a.disabled
			}
		];
	});
}
