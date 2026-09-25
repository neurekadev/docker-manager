// Toasts (#22): short confirmations of a finished action. Copy rule: the
// toast repeats the button's result ("Deploy" → "Deployed Silo", "Save" →
// "Saved compose.yaml"). Errors stay until dismissed; everything else
// disappears after `timeout` ms. Rendered by <Toaster /> in the root layout.

export type ToastTone = 'success' | 'error' | 'info' | 'warn';

export interface ToastAction {
	label: string;
	onclick: () => void;
}

export interface Toast {
	id: number;
	tone: ToastTone;
	title: string;
	body?: string;
	action?: ToastAction;
	/** 0: stays until dismissed. */
	timeout: number;
}

export type ToastOptions = Partial<Pick<Toast, 'body' | 'action' | 'timeout'>>;

type Timer = ReturnType<typeof setTimeout>;

export class Toasts {
	items = $state<Toast[]>([]);
	#next = 1;
	// Timer handles only; not rendered, so not reactive.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#timers = new Map<number, Timer>();
	/** At most this many toasts are shown; the oldest go first. */
	readonly max = 4;

	show(tone: ToastTone, title: string, opts: ToastOptions = {}): number {
		const id = this.#next++;
		const timeout = opts.timeout ?? (tone === 'error' ? 0 : 5000);
		const t: Toast = { id, tone, title, body: opts.body, action: opts.action, timeout };
		this.items = [...this.items, t].slice(-this.max);
		if (timeout > 0)
			this.#timers.set(
				id,
				setTimeout(() => this.dismiss(id), timeout)
			);
		return id;
	}

	success(title: string, opts?: ToastOptions) {
		return this.show('success', title, opts);
	}

	error(title: string, opts?: ToastOptions) {
		return this.show('error', title, opts);
	}

	info(title: string, opts?: ToastOptions) {
		return this.show('info', title, opts);
	}

	warn(title: string, opts?: ToastOptions) {
		return this.show('warn', title, opts);
	}

	dismiss(id: number) {
		const timer = this.#timers.get(id);
		if (timer) clearTimeout(timer);
		this.#timers.delete(id);
		this.items = this.items.filter((t) => t.id !== id);
	}

	clear() {
		for (const t of this.items) this.dismiss(t.id);
	}
}

export const toast = new Toasts();
