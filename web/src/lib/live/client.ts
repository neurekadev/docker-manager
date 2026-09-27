// The live client (#23): one multiplexed EventSource per tab on
// GET /api/v1/live/stream (docs/internal/api/streams.md, "Live invalidation
// stream"), turned into Svelte Query invalidations.
//
//   - hello: when the stream did not resume (first connection, expired or
//     foreign cursor) every cached query is invalidated: the snapshot is
//     refetched and events after the hello cursor keep it current.
//   - Reconnects resume from the last applied cursor (a fresh EventSource
//     cannot send Last-Event-ID, so the cursor goes in the `cursor`
//     parameter) with exponential backoff and full jitter (1 s .. 30 s).
//   - Ids increase strictly: an event whose id is not above the last
//     applied one is a duplicate and ignored.
//   - reset: the cursor expired or events were lost: invalidate everything
//     (or one environment's data) and continue from the new cursor.
//   - permissions.changed: drop every cached query no view shows and
//     refetch the open ones at once (nothing stale and privileged stays),
//     refetch /me/permissions, reconnect.
//   - The browser's "offline" event drops the stream at once (networkLost):
//     an idle connection can stay half-open without an error.
//   - close session_expired: stop; the app signs in again and calls
//     reconnectNow().
//   - Three failed connections within 60 s: bounded polling of the open
//     views (details every 10 s, lists and metrics every 30 s) until the
//     stream is back.
//   - Lists refresh at most every second and metrics every 10 s however
//     many events arrive; details immediately.
//
// It feeds liveStatus (status.svelte.ts) for the shell.
import {
	keyInEnvironment,
	keysForAgent,
	keysForFiles,
	keysForInvalidate,
	keysForJob,
	TOPICS,
	type Invalidation,
	type LiveFilesChanged,
	type LiveInvalidate,
	type LiveJob,
	type LiveReset,
	type QueryKey
} from './keys';
import { liveStatus, type LiveStatus } from './status.svelte';

export const LIVE_URL = '/api/v1/live/stream';
export const LIVE_VERSION = 'docker-manager.live/v1';

/** Minimum time between two refreshes of one key, per refresh class. */
export const REFRESH_MS = { detail: 0, list: 1_000, metrics: 10_000 } as const;
export const POLL_DETAIL_MS = 10_000;
/** Lists and metrics are polled every third detail poll (30 s). */
export const POLL_LIST_EVERY = 3;
export const FAILURE_WINDOW_MS = 60_000;
export const FAILURES_FOR_POLLING = 3;
export const BACKOFF_MIN_MS = 1_000;
export const BACKOFF_MAX_MS = 30_000;

/** The parts of EventSource the client uses (fakeable in tests). */
export interface EventSourceLike {
	addEventListener(type: string, listener: (e: MessageEvent) => void): void;
	onerror: ((e: Event) => void) | null;
	close(): void;
}

export type EventSourceFactory = (url: string) => EventSourceLike;

/** The parts of QueryClient the client uses. */
export interface QueryClientLike {
	invalidateQueries(filters?: {
		queryKey?: QueryKey;
		predicate?: (query: { queryKey: QueryKey }) => boolean;
	}): Promise<void>;
	/** Drops cached queries no view uses (`type: 'inactive'`). */
	removeQueries(filters: { type: 'inactive' }): void;
}

/** Time and timers (fakeable in tests). */
export interface Scheduler {
	now(): number;
	setTimeout(fn: () => void, ms: number): unknown;
	clearTimeout(handle: unknown): void;
	random(): number;
}

const browserScheduler: Scheduler = {
	now: () => Date.now(),
	setTimeout: (fn, ms) => setTimeout(fn, ms),
	clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
	random: () => Math.random()
};

/** What the tab has open: narrows the stream and keeps volumes watched. */
export interface LiveScopes {
	environmentId?: string;
	/** Stack IDs with an open file view. */
	stackIds?: string[];
	/** `<environmentId>/<volume name>` of open volume file views. */
	volumes?: string[];
	/** Template IDs with an open draft file view. */
	templateIds?: string[];
	topics?: string[];
}

export interface LiveClientOptions {
	queryClient: QueryClientLike;
	connect?: EventSourceFactory;
	status?: LiveStatus;
	scheduler?: Scheduler;
	/** Refetch the caller's permissions after a change (before reconnecting). */
	onPermissionsChanged?: () => Promise<unknown> | void;
}

interface Throttled {
	last: number;
	timer: unknown;
}

function parseCursor(c: string): { epoch: string; seq: number } | null {
	const i = c.lastIndexOf('.');
	if (i <= 0) return null;
	const seq = Number(c.slice(i + 1));
	return Number.isSafeInteger(seq) ? { epoch: c.slice(0, i), seq } : null;
}

export class LiveClient {
	readonly #qc: QueryClientLike;
	readonly #connect: EventSourceFactory;
	readonly #status: LiveStatus;
	readonly #clock: Scheduler;
	readonly #onPermissions?: () => Promise<unknown> | void;

	#es: EventSourceLike | null = null;
	#running = false;
	#connected = false;
	/** Position of the last applied event (resume cursor). */
	#cursor: string | null = null;
	#epoch = '';
	#seq = -1;
	#scopes: LiveScopes = {};
	#failures: number[] = [];
	#attempt = 0;
	#reconnectTimer: unknown = null;
	#pollTimer: unknown = null;
	#pollTick = 0;
	#throttled = new Map<string, Throttled>();

	constructor(o: LiveClientOptions) {
		this.#qc = o.queryClient;
		this.#connect =
			o.connect ??
			((url) => new EventSource(url, { withCredentials: true }) as EventSourceLike);
		this.#status = o.status ?? liveStatus;
		this.#clock = o.scheduler ?? browserScheduler;
		this.#onPermissions = o.onPermissionsChanged;
	}

	/** The resume cursor (last applied event), or null. */
	get cursor(): string | null {
		return this.#cursor;
	}

	/** Whether the client is polling instead of streaming. */
	get polling(): boolean {
		return this.#pollTimer !== null;
	}

	start(): void {
		if (this.#running) return;
		this.#running = true;
		this.#open();
	}

	stop(): void {
		this.#running = false;
		this.#closeSource();
		this.#stopPolling();
		this.#clearReconnect();
		for (const t of this.#throttled.values())
			if (t.timer !== null) this.#clock.clearTimeout(t.timer);
		this.#throttled.clear();
		this.#status.set('stopped', this.#clock.now());
	}

	/** Reconnect at once (browser back online, signed in again). */
	reconnectNow(): void {
		this.#running = true;
		this.#attempt = 0;
		this.#clearReconnect();
		this.#closeSource();
		this.#open();
	}

	/**
	 * The browser lost its network (window "offline"). An idle stream can
	 * stay half-open for minutes without an error, so drop it now: the shell
	 * shows the connection loss and the backoff keeps retrying until the
	 * "online" event reconnects at once (resuming from the cursor).
	 */
	networkLost(): void {
		if (!this.#running || this.#es === null) return;
		this.#fail();
	}

	/**
	 * Declares the open views. A change reconnects from the current cursor
	 * (nothing is lost; the server keeps open volumes watched).
	 */
	setScopes(s: LiveScopes): void {
		const norm = (v?: string[]) => [...new Set(v ?? [])].sort();
		const next: LiveScopes = {
			environmentId: s.environmentId,
			stackIds: norm(s.stackIds),
			volumes: norm(s.volumes),
			templateIds: norm(s.templateIds),
			topics: s.topics ? norm(s.topics) : undefined
		};
		if (JSON.stringify(next) === JSON.stringify(this.#scopes)) return;
		this.#scopes = next;
		if (this.#running) this.reconnectNow();
	}

	url(): string {
		const p = new URLSearchParams();
		const s = this.#scopes;
		if (s.topics?.length) p.set('topics', s.topics.join(','));
		if (s.environmentId) p.set('environmentId', s.environmentId);
		if (s.stackIds?.length) p.set('stackId', s.stackIds.join(','));
		if (s.volumes?.length) p.set('volume', s.volumes.join(','));
		if (s.templateIds?.length) p.set('templateId', s.templateIds.join(','));
		if (this.#cursor) p.set('cursor', this.#cursor);
		const q = p.toString();
		return q ? `${LIVE_URL}?${q}` : LIVE_URL;
	}

	#open(): void {
		if (!this.#running) return;
		if (!this.polling)
			this.#status.set(this.#connected ? 'reconnecting' : 'connecting', this.#clock.now());
		const es = this.#connect(this.url());
		this.#es = es;
		const on = (name: string, fn: (data: unknown, id: string) => void) =>
			es.addEventListener(name, (e: MessageEvent) => {
				if (this.#es !== es) return; // a replaced connection
				let data: unknown;
				try {
					data = JSON.parse(String(e.data));
				} catch {
					return;
				}
				this.#status.lastEventAt = this.#clock.now();
				fn(data, e.lastEventId ?? '');
			});
		on('hello', (d) =>
			this.#onHello(d as { cursor: string; resumed: boolean; version: string })
		);
		on(
			'invalidate',
			(d, id) => this.#accept(id) && this.#refreshAll(keysForInvalidate(d as LiveInvalidate))
		);
		on('job', (d, id) => this.#accept(id) && this.#refreshAll(keysForJob(d as LiveJob)));
		on('agent', (d, id) => {
			if (!this.#accept(id)) return;
			const a = d as { environmentId: string; status: 'online' | 'offline' };
			this.#status.environments = {
				...this.#status.environments,
				[a.environmentId]: a.status
			};
			this.#refreshAll(keysForAgent(a.environmentId));
		});
		on(
			'files.changed',
			(d, id) => this.#accept(id) && this.#refreshAll(keysForFiles(d as LiveFilesChanged))
		);
		on('reset', (d) => this.#onReset(d as LiveReset));
		on('permissions.changed', () => void this.#onPermissionsChanged());
		on('close', (d) => this.#onClose((d as { reason: string }).reason));
		es.onerror = () => {
			if (this.#es === es) this.#fail();
		};
	}

	#closeSource(): void {
		const es = this.#es;
		this.#es = null;
		es?.close();
	}

	#clearReconnect(): void {
		if (this.#reconnectTimer !== null) this.#clock.clearTimeout(this.#reconnectTimer);
		this.#reconnectTimer = null;
	}

	#setCursor(c: string): void {
		const p = parseCursor(c);
		if (!p) return;
		this.#cursor = c;
		this.#epoch = p.epoch;
		this.#seq = p.seq;
	}

	/** Applies an event id; false for duplicates (not above the last one). */
	#accept(id: string): boolean {
		const p = parseCursor(id);
		if (!p) return true;
		if (p.epoch === this.#epoch && p.seq <= this.#seq) return false;
		this.#setCursor(id);
		return true;
	}

	#onHello(h: { cursor: string; resumed: boolean; version: string }): void {
		if (h.version !== LIVE_VERSION) {
			// A manager with another schema: poll until the page reloads.
			this.#closeSource();
			this.#startPolling();
			return;
		}
		this.#connected = true;
		this.#attempt = 0;
		this.#failures = [];
		this.#status.failures = 0;
		this.#stopPolling();
		this.#status.set('live', this.#clock.now());
		if (!h.resumed) {
			// Fresh snapshot: everything cached may predate the cursor.
			this.#setCursor(h.cursor);
			void this.#qc.invalidateQueries();
		}
	}

	#onReset(r: LiveReset): void {
		this.#setCursor(r.cursor);
		if (r.environmentId) {
			const env = r.environmentId;
			void this.#qc.invalidateQueries({
				predicate: (q) => keyInEnvironment(q.queryKey, env)
			});
		} else {
			void this.#qc.invalidateQueries();
		}
	}

	async #onPermissionsChanged(): Promise<void> {
		// Nothing fetched with the old permissions may stay: cached data no
		// view shows is dropped now, and every open view refetches at once,
		// so its denied, not-found or narrowed result replaces the old data.
		// Not clear(): mounted views would keep showing what they have. Not a
		// reset: views would render for a moment with no data at all.
		this.#cursor = null;
		this.#epoch = '';
		this.#seq = -1;
		this.#qc.removeQueries({ type: 'inactive' });
		void this.#qc.invalidateQueries();
		await this.#onPermissions?.();
	}

	#onClose(reason: string): void {
		this.#closeSource();
		if (reason === 'session_expired') {
			this.#running = false;
			this.#clearReconnect();
			this.#status.set('unauthenticated', this.#clock.now());
			return;
		}
		// max_age, permissions_changed, shutdown: reconnect at once
		// (shutdown after a short backoff, the manager is restarting).
		if (reason === 'shutdown') this.#scheduleReconnect();
		else this.#open();
	}

	#fail(): void {
		this.#closeSource();
		if (!this.#running) return;
		const now = this.#clock.now();
		this.#failures = [...this.#failures.filter((t) => now - t < FAILURE_WINDOW_MS), now];
		this.#status.failures = this.#failures.length;
		if (this.#failures.length >= FAILURES_FOR_POLLING) this.#startPolling();
		this.#scheduleReconnect();
	}

	#scheduleReconnect(): void {
		this.#clearReconnect();
		this.#attempt++;
		const ceiling = Math.min(BACKOFF_MAX_MS, BACKOFF_MIN_MS * 2 ** (this.#attempt - 1));
		const delay = Math.max(250, Math.floor(this.#clock.random() * ceiling));
		if (!this.polling) this.#status.set('reconnecting', this.#clock.now());
		this.#reconnectTimer = this.#clock.setTimeout(() => {
			this.#reconnectTimer = null;
			this.#open();
		}, delay);
	}

	#startPolling(): void {
		if (this.#pollTimer !== null) return;
		this.#status.set('polling', this.#clock.now());
		this.#pollTick = 0;
		const tick = () => {
			this.#pollTick++;
			const lists = this.#pollTick % POLL_LIST_EVERY === 0;
			void this.#qc.invalidateQueries({
				predicate: (q) => lists || q.queryKey[1] === 'item' || q.queryKey[0] === 'files'
			});
			this.#pollTimer = this.#clock.setTimeout(tick, POLL_DETAIL_MS);
		};
		this.#pollTimer = this.#clock.setTimeout(tick, POLL_DETAIL_MS);
	}

	#stopPolling(): void {
		if (this.#pollTimer !== null) this.#clock.clearTimeout(this.#pollTimer);
		this.#pollTimer = null;
	}

	#refreshAll(list: Invalidation[]): void {
		for (const inv of list) this.#refresh(inv);
	}

	/** Invalidates a key now, or once its refresh interval allows. */
	#refresh(inv: Invalidation): void {
		const wait = REFRESH_MS[inv.class];
		if (wait === 0) {
			void this.#qc.invalidateQueries({ queryKey: inv.key });
			return;
		}
		const id = JSON.stringify(inv.key);
		const now = this.#clock.now();
		const t = this.#throttled.get(id);
		if (t && t.timer !== null) return; // already scheduled
		if (!t || now - t.last >= wait) {
			this.#throttled.set(id, { last: now, timer: null });
			void this.#qc.invalidateQueries({ queryKey: inv.key });
			return;
		}
		t.timer = this.#clock.setTimeout(
			() => {
				t.timer = null;
				t.last = this.#clock.now();
				void this.#qc.invalidateQueries({ queryKey: inv.key });
			},
			wait - (now - t.last)
		);
	}
}

/** Every topic the stream offers. */
export const LIVE_TOPICS: readonly string[] = TOPICS;
