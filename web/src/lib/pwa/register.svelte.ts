// Service-worker registration and the "new version available" flow.
//
// A new build installs in the background and then WAITS. The page shows a
// prompt; only when the user clicks reload does it ask the waiting worker to
// take over and reload once it controls the page. Nothing here reloads on
// its own, so unsaved edits, terminals or restores are never interrupted.
import { SKIP_WAITING } from './sw-core';

export const SW_URL = '/service-worker.js';

/** How often an open tab checks for a new build (the browser also checks on navigation). */
export const UPDATE_CHECK_MS = 60 * 60 * 1000;

type Listener = (ev: Event) => void;

/** The parts of ServiceWorker the flow needs (fakeable in tests). */
export interface WorkerLike {
	state: string;
	postMessage(message: unknown): void;
	addEventListener(type: 'statechange', l: Listener): void;
}

export interface RegistrationLike {
	installing: WorkerLike | null;
	waiting: WorkerLike | null;
	active: WorkerLike | null;
	update(): Promise<unknown>;
	addEventListener(type: 'updatefound', l: Listener): void;
}

export interface ContainerLike {
	controller: unknown;
	register(url: string, options: RegistrationOptions): Promise<RegistrationLike>;
	addEventListener(type: 'controllerchange', l: Listener): void;
	removeEventListener(type: 'controllerchange', l: Listener): void;
}

export class PwaState {
	/** A new build is installed and waiting for the user. */
	updateAvailable = $state(false);
	/** The user accepted the update; waiting for the new worker to take over. */
	updating = $state(false);
	registered = $state(false);
	error = $state<string | null>(null);

	#container: ContainerLike | null = null;
	#registration: RegistrationLike | null = null;
	#reload: () => void = () => location.reload();

	/**
	 * Registers the service worker. `reload` is injectable for tests; it is
	 * only ever called from applyUpdate().
	 */
	async register(
		container: ContainerLike,
		reload?: () => void
	): Promise<RegistrationLike | null> {
		if (reload) this.#reload = reload;
		this.#container = container;
		try {
			const reg = await container.register(SW_URL, {
				scope: '/',
				type: 'classic',
				// Always revalidate the worker script with the manager.
				updateViaCache: 'none'
			});
			this.#registration = reg;
			this.registered = true;
			if (reg.waiting && container.controller) this.updateAvailable = true;
			reg.addEventListener('updatefound', () => this.#track(reg.installing));
			if (reg.installing) this.#track(reg.installing);
			return reg;
		} catch (e) {
			this.error = e instanceof Error ? e.message : String(e);
			return null;
		}
	}

	#track(worker: WorkerLike | null) {
		if (!worker) return;
		worker.addEventListener('statechange', () => {
			// "installed" with an existing controller = an update is waiting.
			// Without a controller it is the first install: nothing to prompt.
			if (worker.state === 'installed' && this.#container?.controller) {
				this.updateAvailable = true;
			}
		});
	}

	/** Asks the browser to look for a new build now. */
	async checkForUpdate(): Promise<void> {
		try {
			await this.#registration?.update();
		} catch {
			// Offline or manager unreachable: the next check retries.
		}
	}

	/**
	 * User accepted the update: activate the waiting worker and reload once
	 * it controls the page. Returns false when there is nothing to apply.
	 */
	applyUpdate(): boolean {
		const container = this.#container;
		const waiting = this.#registration?.waiting;
		if (!container || !waiting) return false;
		this.updating = true;
		const onChange = () => {
			container.removeEventListener('controllerchange', onChange);
			this.#reload();
		};
		container.addEventListener('controllerchange', onChange);
		waiting.postMessage({ type: SKIP_WAITING });
		return true;
	}

	/** Hides the prompt for this page load; the update stays waiting. */
	dismiss(): void {
		this.updateAvailable = false;
	}
}

export const pwa = new PwaState();

/**
 * Browser entry point (called once from the root layout). No-op where
 * service workers are unavailable (plain-HTTP origins other than
 * localhost, some private modes) and in `vite dev`.
 */
export function startServiceWorker(): () => void {
	if (import.meta.env.DEV || !('serviceWorker' in navigator)) return () => {};
	void pwa.register(navigator.serviceWorker as unknown as ContainerLike);
	const check = () => void pwa.checkForUpdate();
	const timer = setInterval(check, UPDATE_CHECK_MS);
	window.addEventListener('online', check);
	return () => {
		clearInterval(timer);
		window.removeEventListener('online', check);
	};
}
