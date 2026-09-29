// Whether this Docker Manager refused a change because it moves to a new
// server (409 manager_moved): the fallback of the session's move lock (a
// session read before the lock began does not know it yet); it holds for
// the page load (a locked manager does not unlock by itself; after a
// cancel the owner reloads).
import { onApiFailure } from '$lib/api/client';

class ManagerMovedSignal {
	/** A request answered 409 manager_moved during this page load. */
	refused = $state(false);
}

export const managerMoved = new ManagerMovedSignal();

/** Listens for manager_moved refusals; returns the function that stops. */
export function watchManagerMoved(signal: ManagerMovedSignal = managerMoved): () => void {
	return onApiFailure((e) => {
		if (e.apiError?.code === 'manager_moved') signal.refused = true;
	});
}
