import { describe, expect, it } from 'vitest';
import {
	PwaState,
	SW_URL,
	type ContainerLike,
	type RegistrationLike,
	type WorkerLike
} from './register.svelte';
import type { CriticalItem } from '$lib/live/critical.svelte';

class FakeWorker extends EventTarget implements WorkerLike {
	state = 'installing';
	messages: unknown[] = [];
	postMessage(m: unknown) {
		this.messages.push(m);
	}
	setState(s: string) {
		this.state = s;
		this.dispatchEvent(new Event('statechange'));
	}
}

class FakeRegistration extends EventTarget implements RegistrationLike {
	installing: FakeWorker | null = null;
	waiting: FakeWorker | null = null;
	active: FakeWorker | null = null;
	updates = 0;
	async update() {
		this.updates++;
	}
	/** Simulates the browser finding and installing a new worker script. */
	newWorker(): FakeWorker {
		const w = new FakeWorker();
		this.installing = w;
		this.dispatchEvent(new Event('updatefound'));
		return w;
	}
	finishInstall(w: FakeWorker) {
		this.installing = null;
		this.waiting = w;
		w.setState('installed');
	}
}

class FakeContainer extends EventTarget implements ContainerLike {
	controller: unknown = null;
	registered: { url: string; options: RegistrationOptions }[] = [];
	constructor(public reg = new FakeRegistration()) {
		super();
	}
	async register(url: string, options: RegistrationOptions) {
		this.registered.push({ url, options });
		return this.reg;
	}
	takeControl(w: FakeWorker) {
		this.controller = w;
		this.dispatchEvent(new Event('controllerchange'));
	}
}

function setup(controlled: boolean) {
	const container = new FakeContainer();
	if (controlled) container.controller = new FakeWorker();
	const state = new PwaState();
	let reloads = 0;
	return { container, state, reload: () => reloads++, reloads: () => reloads };
}

describe('service worker registration', () => {
	it('registers the root-scoped worker and always revalidates its script', async () => {
		const { container, state, reload } = setup(false);
		await state.register(container, reload);
		expect(container.registered).toEqual([
			{ url: SW_URL, options: { scope: '/', type: 'classic', updateViaCache: 'none' } }
		]);
		expect(state.registered).toBe(true);
	});

	it('does not prompt on the first install (no controller yet)', async () => {
		const { container, state, reload, reloads } = setup(false);
		await state.register(container, reload);
		const w = container.reg.newWorker();
		container.reg.finishInstall(w);
		expect(state.updateAvailable).toBe(false);
		// First-install activation takes control without any reload.
		container.takeControl(w);
		expect(reloads()).toBe(0);
	});

	it('prompts when a new build is waiting and never reloads by itself', async () => {
		const { container, state, reload, reloads } = setup(true);
		await state.register(container, reload);
		const w = container.reg.newWorker();
		expect(state.updateAvailable).toBe(false);
		container.reg.finishInstall(w);
		expect(state.updateAvailable).toBe(true);
		// Another tab activating the new worker changes our controller: still no reload.
		container.takeControl(w);
		expect(reloads()).toBe(0);
		expect(w.messages).toEqual([]);
	});

	it('prompts for a worker that was already waiting at page load', async () => {
		const { container, state, reload } = setup(true);
		container.reg.waiting = new FakeWorker();
		await state.register(container, reload);
		expect(state.updateAvailable).toBe(true);
	});

	it('applies the update only on request: skip waiting, then reload once controlled', async () => {
		const { container, state, reload, reloads } = setup(true);
		await state.register(container, reload);
		const w = container.reg.newWorker();
		container.reg.finishInstall(w);

		expect(state.applyUpdate()).toBe(true);
		expect(state.updating).toBe(true);
		expect(w.messages).toEqual([{ type: 'SKIP_WAITING' }]);
		expect(reloads()).toBe(0); // not before the new worker controls the page
		container.takeControl(w);
		expect(reloads()).toBe(1);
		container.takeControl(new FakeWorker());
		expect(reloads()).toBe(1); // one reload only
	});

	it('never reloads while critical work is open (#23)', async () => {
		const container = new FakeContainer();
		container.controller = new FakeWorker();
		const open: CriticalItem[] = [{ id: 1, kind: 'unsaved-edit', label: 'compose.yaml' }];
		const state = new PwaState(() => open);
		let reloads = 0;
		await state.register(container, () => reloads++);
		const w = container.reg.newWorker();
		container.reg.finishInstall(w);

		expect(state.applyUpdate()).toBe(false);
		expect(state.blockedBy.map((i) => i.label)).toEqual(['compose.yaml']);
		expect(w.messages).toEqual([]);
		expect(state.updating).toBe(false);

		// Work finished: the update applies.
		open.length = 0;
		expect(state.applyUpdate()).toBe(true);
		expect(state.blockedBy).toEqual([]);
		// A terminal opened while the new worker was activating: still no reload.
		open.push({ id: 2, kind: 'terminal', label: 'web-1' });
		container.takeControl(w);
		expect(reloads).toBe(0);
		expect(state.updating).toBe(false);
		expect(state.blockedBy.map((i) => i.kind)).toEqual(['terminal']);
		// Terminal closed: the next click reloads (the worker is active).
		open.length = 0;
		container.reg.waiting = null;
		expect(state.applyUpdate()).toBe(true);
		expect(reloads).toBe(1);
	});

	it('applyUpdate is a no-op without a waiting worker', async () => {
		const { container, state, reload } = setup(true);
		await state.register(container, reload);
		expect(state.applyUpdate()).toBe(false);
		expect(state.updating).toBe(false);
	});

	it('dismiss hides the prompt without applying the update', async () => {
		const { container, state, reload, reloads } = setup(true);
		await state.register(container, reload);
		const w = container.reg.newWorker();
		container.reg.finishInstall(w);
		state.dismiss();
		expect(state.updateAvailable).toBe(false);
		expect(w.messages).toEqual([]);
		expect(reloads()).toBe(0);
	});

	it('checkForUpdate asks the registration and tolerates failures', async () => {
		const { container, state, reload } = setup(true);
		await state.register(container, reload);
		await state.checkForUpdate();
		expect(container.reg.updates).toBe(1);
		container.reg.update = async () => {
			throw new TypeError('offline');
		};
		await expect(state.checkForUpdate()).resolves.toBeUndefined();
	});

	it('records registration errors', async () => {
		const { container, state, reload } = setup(false);
		container.register = async () => {
			throw new Error('SecurityError: insecure origin');
		};
		expect(await state.register(container, reload)).toBeNull();
		expect(state.error).toContain('insecure origin');
		expect(state.registered).toBe(false);
	});
});
