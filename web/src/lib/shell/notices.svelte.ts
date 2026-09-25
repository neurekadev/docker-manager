// In-app notices (#22 bell, #25 Q6: in-app only in v1): job finished or
// failed, update available, environment offline. Sources push notices; the
// bell shows the unread count and the list. Notices live in memory for the
// tab only (no persistence of API data). Keys deduplicate: pushing the same
// key again updates the notice instead of adding one.

export type NoticeKind = 'job' | 'environment' | 'update';
export type NoticeTone = 'ok' | 'warn' | 'danger' | 'info';

export interface AppNotice {
	key: string;
	kind: NoticeKind;
	tone: NoticeTone;
	title: string;
	body?: string;
	href?: string;
	at: number;
	read: boolean;
}

export class Notices {
	items = $state<AppNotice[]>([]);
	readonly unread = $derived(this.items.filter((n) => !n.read).length);
	/** Newest first, at most this many. */
	readonly max = 50;
	#now: () => number;

	constructor(now: () => number = () => Date.now()) {
		this.#now = now;
	}

	push(n: Omit<AppNotice, 'at' | 'read'> & { at?: number }) {
		const notice: AppNotice = { ...n, at: n.at ?? this.#now(), read: false };
		this.items = [notice, ...this.items.filter((x) => x.key !== n.key)].slice(0, this.max);
	}

	/** Removes a notice whose condition ended (environment back online). */
	resolve(key: string) {
		this.items = this.items.filter((n) => n.key !== key);
	}

	markAllRead() {
		if (this.unread) this.items = this.items.map((n) => (n.read ? n : { ...n, read: true }));
	}

	clear() {
		this.items = [];
	}
}

export const notices = new Notices();

/**
 * Tracks environment online/offline transitions from successive
 * environment lists and pushes/resolves "offline" notices. Returns the
 * function to feed with each new list.
 */
export function environmentNotices(target: Notices = notices) {
	let seen: Map<string, boolean> | null = null;
	return (envs: readonly { id: string; name: string; online: boolean }[]) => {
		const first = seen === null;
		// Plain bookkeeping between calls, not reactive state.
		// eslint-disable-next-line svelte/prefer-svelte-reactivity
		const next = new Map(envs.map((e) => [e.id, e.online]));
		for (const e of envs) {
			const was = seen?.get(e.id);
			const key = `environment-offline:${e.id}`;
			if (!e.online && (first || was !== false)) {
				target.push({
					key,
					kind: 'environment',
					tone: 'warn',
					title: `${e.name} is offline`,
					body: 'Its agent is not connected. DockYard shows its last known state.',
					href: `/environments/${encodeURIComponent(e.id)}`
				});
			} else if (e.online && was === false) {
				target.resolve(key);
			}
		}
		seen = next;
	};
}
