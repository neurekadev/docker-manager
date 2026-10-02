// The log viewer's regular expression search (#8): runs the pattern in a
// worker (regex.worker.ts) over the lines it has not searched yet and keeps
// each line's result by its seq, so new lines while following cost only
// themselves. JavaScript cannot interrupt a running regular expression, so
// a batch that takes longer than the time limit (a pattern that backtracks
// for minutes, such as (a|aa)+$) terminates the worker: the pattern is
// reported as too slow instead of freezing the page.
import type { LogLine } from './feed.svelte';
import type { Matcher, Pattern, Range } from './format';

/** The parts of Worker the search uses (fakeable in tests). */
export interface WorkerLike {
	postMessage(message: unknown): void;
	terminate(): void;
	onmessage: ((e: MessageEvent) => void) | null;
}

export interface RegexRequest {
	id: number;
	pattern: Pattern;
	texts: string[];
}

export interface RegexReply {
	id: number;
	results: (Range[] | null)[];
}

export interface RegexSearchOptions {
	/** A batch running longer than this stops the pattern (default 2 s). */
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
	/** Lines are being searched. */
	busy = $state(false);

	#make: () => WorkerLike;
	#opts: RegexSearchOptions;
	#worker: WorkerLike | null = null;
	#key = '';
	#pattern: Pattern | null = null;
	// The current pattern's result per line seq: bookkeeping, read through
	// matcher() after `version` changes.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#results = new Map<number, Range[] | null>();
	#inflight: { id: number; seqs: number[]; timer: unknown } | null = null;
	/** The lines to search once the running batch ends. */
	#next: readonly LogLine[] | null = null;
	#id = 0;

	constructor(make: () => WorkerLike, opts: RegexSearchOptions = {}) {
		this.#make = make;
		this.#opts = opts;
	}

	/** Searches the lines for the pattern (only the ones not searched yet). */
	run(pattern: Pattern, lines: readonly LogLine[]) {
		const key = keyOf(pattern);
		if (key !== this.#key) {
			// A new pattern: the old one's batch may never end.
			if (this.#inflight) this.#stop();
			this.#next = null;
			this.#key = key;
			this.#pattern = pattern;
			this.#results.clear();
			this.slow = false;
			this.version++;
		}
		if (this.slow) return;
		if (this.#inflight) this.#next = lines;
		else this.#send(lines);
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
		this.#next = null;
	}

	#send(lines: readonly LogLine[]) {
		if (this.#results.size > lines.length * 2) {
			// Forget the lines that left the buffer.
			const kept = new Set(lines.map((l) => l.seq));
			for (const seq of [...this.#results.keys()])
				if (!kept.has(seq)) this.#results.delete(seq);
		}
		const todo = lines.filter((l) => !this.#results.has(l.seq));
		if (!todo.length) {
			this.busy = false;
			return;
		}
		const id = ++this.#id;
		const worker = (this.#worker ??= this.#create());
		const set = this.#opts.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
		const timer = set(() => this.#timeout(id), this.#opts.timeoutMs ?? 2000);
		this.#inflight = { id, seqs: todo.map((l) => l.seq), timer };
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
		w.onmessage = (e) => this.#reply(e.data as RegexReply);
		return w;
	}

	#reply(r: RegexReply) {
		const f = this.#inflight;
		if (!f || f.id !== r.id) return;
		this.#clear(f.timer);
		this.#inflight = null;
		f.seqs.forEach((seq, i) => this.#results.set(seq, r.results[i] ?? null));
		this.version++;
		const next = this.#next;
		this.#next = null;
		if (next) this.#send(next);
		else this.busy = false;
	}

	#timeout(id: number) {
		if (this.#inflight?.id !== id) return;
		this.#stop();
		this.#next = null;
		this.slow = true;
		this.version++;
	}

	#stop() {
		if (this.#inflight) this.#clear(this.#inflight.timer);
		this.#inflight = null;
		this.#worker?.terminate();
		this.#worker = null;
		this.busy = false;
	}

	#clear(t: unknown) {
		(this.#opts.clearTimer ?? ((x) => clearTimeout(x as ReturnType<typeof setTimeout>)))(t);
	}
}
