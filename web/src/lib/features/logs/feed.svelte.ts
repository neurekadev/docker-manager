// Log feed (#8, docs/api/streams.md "Container logs"): follows one or more
// containers' logs over SSE (GET …/logs/stream) and merges them by time.
//
//   - each `log` event's id is its RFC 3339 nano timestamp: the cursor. A
//     reconnect (EventSource's own, or ours after Follow is turned back on
//     or the stream ended) resumes at the cursor with `since` and skips the
//     lines already shown with exactly that timestamp (dedupe by id+line).
//   - `dropped {count}`: the manager dropped lines because the browser fell
//     behind; the view says how many.
//   - `end {reason}` (container_removed, permissions_changed, agent_offline)
//     ends a source; `close {reason}` (max_age) lets EventSource reconnect;
//     session_expired stops.
//   - the buffer is bounded (MAX_LINES, oldest dropped first); nothing is
//     stored beyond the page (log lines can hold secrets).
//   - over HTTP/1.1 a browser keeps six connections per host: containers
//     beyond `maxStreams` are followed by polling GET …/logs?since=<cursor>
//     (same cursor and dedupe), so a stack with many services never starves
//     the page's API calls.

import { streamUrl, timeKey, type LogSource } from './stream';

export { endReason, streamUrl, timeKey, type LogSource } from './stream';

export const MAX_LINES = 5000;

export interface LogLine {
	seq: number;
	source: string;
	/** RFC 3339 nano timestamp (the event id). */
	at: string;
	/** timeKey(at): sortable. */
	key: string;
	stream: 'stdout' | 'stderr';
	text: string;
	partial?: boolean;
}

export type SourceState =
	| { kind: 'connecting' }
	| { kind: 'live' }
	| { kind: 'paused' }
	| { kind: 'reconnecting' }
	| { kind: 'ended'; reason: string }
	| { kind: 'failed'; status: number | null; message: string };

/** The parts of EventSource the feed uses (fakeable in tests). */
export interface EventSourceLike {
	readonly readyState: number;
	addEventListener(type: string, listener: (ev: MessageEvent) => void): void;
	close(): void;
	onerror: ((ev: Event) => void) | null;
	onopen?: ((ev: Event) => void) | null;
}

export interface LogFeedOptions {
	eventSource?: (url: string) => EventSourceLike;
	/** Lines of history per container when a stream starts (default 200). */
	tail?: number;
	max?: number;
	/** Classifies a stream that could not be opened (HTTP status of a probe). */
	probe?: (source: LogSource) => Promise<{ status: number | null; message: string }>;
	/** Retry delay after an ended or failed stream (ms; 0 disables). */
	retryMs?: number;
	setTimer?: (fn: () => void, ms: number) => unknown;
	clearTimer?: (t: unknown) => void;
	/**
	 * At most this many SSE streams at once. Browsers allow six HTTP/1.1
	 * connections per host (the live stream holds one; API calls need the
	 * rest), so over HTTP/1.1 the remaining containers are followed by
	 * polling (`poll`) instead of streaming. Unlimited by default.
	 */
	maxStreams?: number;
	/** Reads a container's lines after `since` (or its tail): GET …/logs. */
	poll?: (source: LogSource, since?: string) => Promise<RawLine[]>;
	/** Poll interval (default 3 s). */
	pollMs?: number;
}

/** One line as the API sends it. */
export interface RawLine {
	at: string;
	stream: 'stdout' | 'stderr';
	line: string;
	partial?: boolean;
}

interface Cursor {
	at: string;
	/** Lines already shown with exactly `at` (dedupe after a resume). */
	seen: string[];
}

export class LogFeed {
	lines = $state.raw<LogLine[]>([]);
	states = $state<Record<string, SourceState>>({});
	/** Lines the manager dropped because this view fell behind. */
	dropped = $state(0);
	/** Lines discarded from the top of the bounded buffer. */
	trimmed = $state(0);
	following = $state(true);
	/** The followed containers. */
	sources = $state.raw<LogSource[]>([]);

	#opts: LogFeedOptions;
	// Streams, cursors and retry timers are bookkeeping, never rendered.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#streams = new Map<string, EventSourceLike>();
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#cursors = new Map<string, Cursor>();
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#timers = new Map<string, unknown>();
	/** Sources followed by polling (over the stream budget). */
	#polling: string[] = [];
	#seq = 0;
	#stopped = true;

	constructor(sources: LogSource[], opts: LogFeedOptions = {}) {
		this.sources = sources;
		this.#opts = opts;
	}

	start() {
		this.#stopped = false;
		for (const s of this.sources) this.#open(s);
	}

	stop() {
		this.#stopped = true;
		for (const k of [...this.#streams.keys(), ...this.#polling]) this.#close(k);
		for (const t of this.#timers.values()) this.#clear(t);
		this.#timers.clear();
	}

	/** Whether a source is followed by polling instead of a stream. */
	polled(key: string): boolean {
		return this.#polling.includes(key);
	}

	/** Replaces the followed containers (a stack's services changed). */
	setSources(next: LogSource[]) {
		const keep = next.map((s) => s.key);
		for (const s of this.sources) if (!keep.includes(s.key)) this.#close(s.key);
		const before = this.sources.map((s) => s.key);
		this.sources = next;
		if (!this.#stopped && this.following)
			for (const s of next) if (!before.includes(s.key)) this.#open(s);
	}

	/** Follow off: stop streaming (the view stays); on: resume at the cursors. */
	setFollowing(on: boolean) {
		if (on === this.following) return;
		this.following = on;
		if (this.#stopped) return;
		if (on) for (const s of this.sources) this.#open(s);
		else
			for (const s of this.sources) {
				this.#close(s.key);
				this.#state(s.key, { kind: 'paused' });
			}
	}

	/** Clears the view only (the streams continue after the cursor). */
	clear() {
		this.lines = [];
		this.dropped = 0;
		this.trimmed = 0;
	}

	/** Reopens a source that ended or failed. */
	retry(key: string) {
		const s = this.sources.find((x) => x.key === key);
		if (s && !this.#stopped) this.#open(s);
	}

	#state(key: string, st: SourceState) {
		this.states = { ...this.states, [key]: st };
	}

	#clear(t: unknown) {
		(this.#opts.clearTimer ?? ((x) => clearTimeout(x as ReturnType<typeof setTimeout>)))(t);
	}

	#close(key: string) {
		this.#streams.get(key)?.close();
		this.#streams.delete(key);
		this.#polling = this.#polling.filter((k) => k !== key);
		const t = this.#timers.get(key);
		if (t !== undefined) this.#clear(t);
		this.#timers.delete(key);
	}

	#open(s: LogSource) {
		this.#close(s.key);
		if (this.#opts.poll && this.#streams.size >= (this.#opts.maxStreams ?? Infinity)) {
			this.#startPolling(s);
			return;
		}
		const cursor = this.#cursors.get(s.key);
		const url = streamUrl(s, this.#opts.tail ?? 200, cursor?.at);
		const factory =
			this.#opts.eventSource ??
			((u: string) =>
				new EventSource(u, { withCredentials: true }) as unknown as EventSourceLike);
		let es: EventSourceLike;
		try {
			es = factory(url);
		} catch (e) {
			this.#state(s.key, { kind: 'failed', status: null, message: String(e) });
			return;
		}
		this.#streams.set(s.key, es);
		this.#state(s.key, { kind: cursor ? 'reconnecting' : 'connecting' });
		es.onopen = () => this.#state(s.key, { kind: 'live' });
		es.addEventListener('log', (ev) => {
			if (this.states[s.key]?.kind !== 'live') this.#state(s.key, { kind: 'live' });
			this.#onLine(s.key, ev);
		});
		es.addEventListener('dropped', (ev) => {
			try {
				this.dropped += (JSON.parse(ev.data) as { count: number }).count ?? 0;
			} catch {
				// malformed: ignore
			}
		});
		es.addEventListener('end', (ev) => {
			let reason = '';
			try {
				reason = (JSON.parse(ev.data) as { reason: string }).reason;
			} catch {
				// keep ''
			}
			this.#close(s.key);
			this.#state(s.key, { kind: 'ended', reason });
			if (reason === 'agent_offline') this.#retryLater(s);
		});
		es.addEventListener('close', (ev) => {
			let reason = '';
			try {
				reason = (JSON.parse(ev.data) as { reason: string }).reason;
			} catch {
				// keep ''
			}
			if (reason === 'session_expired' || reason === 'permissions_changed') {
				this.#close(s.key);
				this.#state(s.key, { kind: 'ended', reason });
			}
			// max_age / shutdown: EventSource reconnects with Last-Event-ID.
		});
		es.onerror = () => {
			if (es.readyState === 2) {
				// The browser gave up: an HTTP error before the stream (403,
				// 404, 503) or repeated failures. Ask the API what it was.
				this.#streams.delete(s.key);
				const probe = this.#opts.probe;
				if (!probe) {
					this.#state(s.key, {
						kind: 'failed',
						status: null,
						message: 'The log stream could not be opened.'
					});
					return;
				}
				void probe(s).then((r) => {
					if (this.#stopped || this.#streams.has(s.key)) return;
					this.#state(s.key, { kind: 'failed', ...r });
					if (r.status === null || r.status >= 500) this.#retryLater(s);
				});
			} else this.#state(s.key, { kind: 'reconnecting' });
		};
	}

	/** Follows a source with GET …/logs?since=<cursor> every pollMs. */
	#startPolling(s: LogSource) {
		const poll = this.#opts.poll!;
		const key = s.key;
		this.#polling = [...this.#polling, key];
		this.#state(key, { kind: this.#cursors.has(key) ? 'reconnecting' : 'connecting' });
		const set = this.#opts.setTimer ?? ((fn, t) => setTimeout(fn, t));
		let failures = 0;
		const tick = async () => {
			this.#timers.delete(key);
			if (this.#stopped || !this.following || !this.#polling.includes(key)) return;
			try {
				const lines = await poll(s, this.#cursors.get(key)?.at);
				if (!this.#polling.includes(key)) return; // closed meanwhile
				for (const l of lines) this.#ingest(key, l.at, l);
				failures = 0;
				if (this.states[key]?.kind !== 'live') this.#state(key, { kind: 'live' });
			} catch (e) {
				if (!this.#polling.includes(key)) return;
				const status = (e as { status?: number | null }).status ?? null;
				const message = e instanceof Error ? e.message : String(e);
				failures++;
				// Refusals are final; outages are retried a few times first.
				if ((status !== null && status < 500) || failures >= 3) {
					this.#close(key);
					this.#state(key, { kind: 'failed', status, message });
					if (status === null || status >= 500) this.#retryLater(s);
					return;
				}
				this.#state(key, { kind: 'reconnecting' });
			}
			this.#timers.set(
				key,
				set(() => void tick(), this.#opts.pollMs ?? 3000)
			);
		};
		void tick();
	}

	#retryLater(s: LogSource) {
		const ms = this.#opts.retryMs ?? 10_000;
		if (!ms || this.#stopped) return;
		const set = this.#opts.setTimer ?? ((fn, t) => setTimeout(fn, t));
		this.#timers.set(
			s.key,
			set(() => {
				this.#timers.delete(s.key);
				if (!this.#stopped && this.following) this.#open(s);
			}, ms)
		);
	}

	#onLine(key: string, ev: MessageEvent) {
		let d: RawLine;
		try {
			d = JSON.parse(ev.data);
		} catch {
			return;
		}
		this.#ingest(key, ev.lastEventId || d.at, d);
	}

	/** Adds a line unless the cursor shows it was delivered already. */
	#ingest(key: string, at: string, d: RawLine) {
		const k = timeKey(at);
		const cur = this.#cursors.get(key);
		if (cur) {
			const ck = timeKey(cur.at);
			if (k < ck) return; // replayed older line
			if (k === ck) {
				if (cur.seen.includes(d.line)) return;
				cur.seen.push(d.line);
			} else this.#cursors.set(key, { at, seen: [d.line] });
		} else this.#cursors.set(key, { at, seen: [d.line] });
		this.#append({
			seq: ++this.#seq,
			source: key,
			at,
			key: k,
			stream: d.stream,
			text: d.line,
			partial: d.partial
		});
	}

	#append(l: LogLine) {
		const max = this.#opts.max ?? MAX_LINES;
		let next: LogLine[];
		const last = this.lines[this.lines.length - 1];
		if (!last || last.key <= l.key) next = [...this.lines, l];
		else {
			// Another container's line from slightly earlier: insert by time.
			let lo = 0;
			let hi = this.lines.length;
			while (lo < hi) {
				const mid = (lo + hi) >> 1;
				if (this.lines[mid].key <= l.key) lo = mid + 1;
				else hi = mid;
			}
			next = [...this.lines.slice(0, lo), l, ...this.lines.slice(lo)];
		}
		if (next.length > max) {
			this.trimmed += next.length - max;
			next = next.slice(next.length - max);
		}
		this.lines = next;
	}
}
