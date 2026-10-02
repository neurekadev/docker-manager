// The log viewer's regular expression search (#8): runs the pattern in a
// worker (regex.worker.ts) over the lines it has not searched yet, in
// chunks of at most CHUNK lines, and keeps each line's result by its seq,
// so new lines while following cost only themselves. JavaScript cannot
// interrupt a running regular expression, so a chunk that takes longer
// than the time limit (a pattern that backtracks for minutes, such as
// (a|aa)+$) terminates the worker: the pattern is reported as too slow
// instead of freezing the page. The limit starts once the worker has
// loaded, so a cold start never counts against the pattern. A pattern that
// throws in the worker, or a worker that fails, makes the search
// unavailable for that pattern; the next pattern tries again.
import type { LogLine } from './feed.svelte';
import type { Matcher, Pattern, Range } from './format';

/** Lines per request: each chunk gets the whole time limit. */
export const CHUNK = 1000;

/** The parts of Worker the search uses (fakeable in tests). */
export interface WorkerLike {
	postMessage(message: unknown): void;
	terminate(): void;
	onmessage: ((e: MessageEvent) => void) | null;
	onerror: ((e: ErrorEvent) => void) | null;
}

export interface RegexRequest {
	id: number;
	pattern: Pattern;
	texts: string[];
}

/** The worker's messages: `ready` once loaded, then one reply per request. */
export type RegexReply =
	{ ready: true } | { id: number; results: (Range[] | null)[] } | { id: number; failed: true };

export interface RegexSearchOptions {
	/** A chunk running longer than this stops the pattern (default 2 s). */
	timeoutMs?: number;
	setTimer?: (fn: () => void, ms: number) => unknown;
	clearTimer?: (t: unknown) => void;
}

const keyOf = (p: Pattern) => `${p.flags}/${p.source}`;

export class RegexSearch {
	/** Bumped whenever the results change (the view filters again). */
	version = $state(0);
	/** The current pattern ran past the time limit and was stopped. */
	slow = $state(false);
	/** The current pattern threw in the worker, or the worker failed. */
	failed = $state(false);
	/** Lines are being searched. */
	busy = $state(false);

	#make: () => WorkerLike;
	#opts: RegexSearchOptions;
	#worker: WorkerLike | null = null;
	#ready = false;
	#key = '';
	#pattern: Pattern | null = null;
	/** The lines to search: the latest ones run() received. */
	#lines: readonly LogLine[] = [];
	// The current pattern's result per line seq: bookkeeping, read through
	// matcher() after `version` changes.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#results = new Map<number, Range[] | null>();
	#inflight: { id: number; seqs: number[]; timer: unknown } | null = null;
	#id = 0;

	constructor(make: () => WorkerLike, opts: RegexSearchOptions = {}) {
		this.#make = make;
		this.#opts = opts;
	}

	/** Searches the lines for the pattern (only the ones not searched yet). */
	run(pattern: Pattern, lines: readonly LogLine[]) {
		const key = keyOf(pattern);
		if (key !== this.#key) {
			// A new pattern: the old one's chunk may never end.
			if (this.#inflight) this.#stop();
			this.#key = key;
			this.#pattern = pattern;
			this.#results.clear();
			this.slow = false;
			this.failed = false;
			this.version++;
		}
		this.#lines = lines;
		if (this.slow || this.failed || this.#inflight) return;
		this.#send();
	}

	/** The results so far as a matcher (lines not searched yet do not match). */
	matcher(): Matcher {
		const results = this.#results;
		return {
			test: (l) => results.get(l.seq) != null,
			ranges: (l) => results.get(l.seq) ?? []
		};
	}

	dispose() {
		this.#stop();
	}

	#send() {
		const lines = this.#lines;
		if (this.#results.size > lines.length * 2) {
			// Forget the lines that left the buffer (a local lookup, not state).
			// eslint-disable-next-line svelte/prefer-svelte-reactivity
			const kept = new Set(lines.map((l) => l.seq));
			for (const seq of [...this.#results.keys()])
				if (!kept.has(seq)) this.#results.delete(seq);
		}
		const todo: LogLine[] = [];
		for (const l of lines) {
			if (!this.#results.has(l.seq)) todo.push(l);
			if (todo.length === CHUNK) break;
		}
		if (!todo.length) {
			this.busy = false;
			return;
		}
		const id = ++this.#id;
		const worker = (this.#worker ??= this.#create());
		this.#inflight = { id, seqs: todo.map((l) => l.seq), timer: null };
		// A worker still loading queues the request; its limit starts at `ready`.
		if (this.#ready) this.#startTimer();
		this.busy = true;
		const request: RegexRequest = {
			id,
			pattern: this.#pattern!,
			texts: todo.map((l) => l.text)
		};
		worker.postMessage(request);
	}

	#create(): WorkerLike {
		const w = this.#make();
		this.#ready = false;
		w.onmessage = (e) => this.#reply(e.data as RegexReply);
		w.onerror = () => this.#fail();
		return w;
	}

	#startTimer() {
		const f = this.#inflight;
		if (!f || f.timer !== null) return;
		const set = this.#opts.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
		f.timer = set(() => this.#timeout(f.id), this.#opts.timeoutMs ?? 2000);
	}

	#reply(r: RegexReply) {
		if ('ready' in r) {
			this.#ready = true;
			this.#startTimer();
			return;
		}
		const f = this.#inflight;
		if (!f || f.id !== r.id) return;
		this.#clear(f.timer);
		this.#inflight = null;
		if ('failed' in r) {
			// The pattern threw; the worker itself is fine for the next one.
			this.busy = false;
			this.failed = true;
			this.version++;
			return;
		}
		f.seqs.forEach((seq, i) => this.#results.set(seq, r.results[i] ?? null));
		this.version++;
		// The next chunk, or the lines that arrived meanwhile.
		this.#send();
	}

	#timeout(id: number) {
		if (this.#inflight?.id !== id) return;
		this.#stop();
		this.slow = true;
		this.version++;
	}

	#fail() {
		this.#stop();
		this.failed = true;
		this.version++;
	}

	#stop() {
		if (this.#inflight) this.#clear(this.#inflight.timer);
		this.#inflight = null;
		this.#worker?.terminate();
		this.#worker = null;
		this.#ready = false;
		this.busy = false;
	}

	#clear(t: unknown) {
		if (t === null) return;
		(this.#opts.clearTimer ?? ((x) => clearTimeout(x as ReturnType<typeof setTimeout>)))(t);
	}
}
