// Connection state for the offline indicator (#11, #23).
//
// Two independent signals: the browser's own online/offline state, and
// whether the manager answered the last API request. Either failing means
// live data is unavailable. Nothing is queued while offline: Docker
// mutations are only ever sent by an explicit user action while online.
import { ApiRequestError } from '$lib/api/client';

const GATEWAY_STATUSES = new Set([502, 503, 504]);

export type ConnectionState = 'online' | 'offline' | 'manager-unreachable';

interface WindowLike {
	addEventListener(type: 'online' | 'offline', l: () => void): void;
	removeEventListener(type: 'online' | 'offline', l: () => void): void;
}

export class Connectivity {
	browserOnline = $state(true);
	managerReachable = $state(true);

	readonly state: ConnectionState = $derived(
		!this.browserOnline ? 'offline' : !this.managerReachable ? 'manager-unreachable' : 'online'
	);

	/** Follows the browser's online/offline events; returns a cleanup function. */
	watch(target: WindowLike, initiallyOnline: boolean): () => void {
		this.browserOnline = initiallyOnline;
		const on = () => (this.browserOnline = true);
		const off = () => (this.browserOnline = false);
		target.addEventListener('online', on);
		target.addEventListener('offline', off);
		return () => {
			target.removeEventListener('online', on);
			target.removeEventListener('offline', off);
		};
	}

	/**
	 * Feeds the outcome of an API request. A DockYard answer (success or a
	 * DockYard error body) proves the manager is reachable; a network failure
	 * or a bare gateway error from the reverse proxy (502/503/504 without the
	 * DockYard error shape) means it is not.
	 */
	observe(outcome: { ok: true } | { ok: false; error: unknown }): void {
		if (outcome.ok) {
			this.managerReachable = true;
			return;
		}
		const e = outcome.error;
		if (!(e instanceof ApiRequestError)) return;
		this.managerReachable = !(e.network || (!e.apiError && GATEWAY_STATUSES.has(e.status!)));
	}
}

export const connectivity = new Connectivity();
