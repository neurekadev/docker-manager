// The selected environment (#22 environment switcher): one environment ID
// or null for "All environments", remembered per user.
//
// Decision (#22): there is no user-preferences API in v1, so the choice is
// kept in localStorage under docker-manager:environment:<userId>, holding the
// environment ID only (not sensitive; no API data, no tokens). A stored ID
// the user can no longer see falls back to "All environments". Lists read
// `environmentSelection.id` to filter; breadcrumbs show the environment as
// the first crumb when one is selected.

export interface StorageLike {
	getItem(key: string): string | null;
	setItem(key: string, value: string): void;
	removeItem(key: string): void;
}

const PREFIX = 'docker-manager:environment:';

function browserStorage(): StorageLike | null {
	try {
		return typeof window === 'undefined' ? null : window.localStorage;
	} catch {
		return null; // storage disabled (private mode policies)
	}
}

export class EnvironmentSelection {
	/** The selected environment ID; null = All environments. */
	id = $state<string | null>(null);
	#user: string | null = null;
	#storage: StorageLike | null;

	constructor(storage: StorageLike | null = browserStorage()) {
		this.#storage = storage;
	}

	/**
	 * Restores the user's choice once their visible environments are known.
	 * Call again when the visible set changes (a revoked environment is
	 * deselected).
	 */
	restore(userId: string, visibleIds: readonly string[]) {
		this.#user = userId;
		const stored = this.#read(userId);
		const want = this.id ?? stored;
		this.id = want && visibleIds.includes(want) ? want : null;
	}

	#read(userId: string): string | null {
		try {
			return this.#storage?.getItem(PREFIX + userId) ?? null;
		} catch {
			return null;
		}
	}

	select(id: string | null) {
		this.id = id;
		if (!this.#user) return;
		try {
			if (id) this.#storage?.setItem(PREFIX + this.#user, id);
			else this.#storage?.removeItem(PREFIX + this.#user);
		} catch {
			// Not persisted; the choice still applies to this tab.
		}
	}

	/** Forget the in-memory choice (sign-out); the stored one stays per user. */
	reset() {
		this.id = null;
		this.#user = null;
	}
}

export const environmentSelection = new EnvironmentSelection();
