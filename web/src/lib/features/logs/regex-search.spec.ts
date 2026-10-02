// Regular expression search of the log viewer (#8): chunks in a worker,
// results kept per line, new lines only, a time limit that starts once the
// worker has loaded and stops patterns which backtrack for minutes.
import { describe, expect, it } from 'vitest';
import type { LogLine } from './feed.svelte';
import { regexMatches } from './format';
import {
	CHUNK,
	RegexSearch,
	type RegexReply,
	type RegexRequest,
	type WorkerLike
} from './regex-search.svelte';

class FakeWorker implements WorkerLike {
	static all: FakeWorker[] = [];
	onmessage: ((e: MessageEvent) => void) | null = null;
	onerror: ((e: ErrorEvent) => void) | null = null;
	requests: RegexRequest[] = [];
	terminated = false;

	constructor() {
		FakeWorker.all.push(this);
	}
	postMessage(m: unknown) {
		this.requests.push(m as RegexRequest);
	}
	terminate() {
		this.terminated = true;
	}
	#send(reply: RegexReply) {
		this.onmessage?.({ data: reply } as MessageEvent);
	}
	/** The worker has loaded. */
	ready() {
		this.#send({ ready: true });
	}
	/** Answers the last request the way the real worker does. */
	answer() {
		const r = this.requests.at(-1)!;
		this.#send({ id: r.id, results: regexMatches(r.pattern, r.texts) });
	}
}

const line = (seq: number, text: string): LogLine => ({
	seq,
	source: 'k',
	at: `T${seq}`,
	key: `T${seq}`,
	stream: 'stdout',
	text,
	level: 'other'
});

function setup() {
	FakeWorker.all = [];
	const timers: { fn: () => void; cleared: boolean }[] = [];
	const search = new RegexSearch(() => new FakeWorker(), {
		timeoutMs: 1000,
		setTimer: (fn) => {
			const t = { fn, cleared: false };
			timers.push(t);
			return t;
		},
		clearTimer: (t) => ((t as { cleared: boolean }).cleared = true)
	});
	return { search, timers };
}

const p500 = { source: String.raw`5\d\d`, flags: 'gi' };

describe('RegexSearch', () => {
	it('searches in the worker, then only the lines it has not searched', () => {
		const { search } = setup();
		const lines = [line(1, 'GET / 200'), line(2, 'GET /login 500')];
		search.run(p500, lines);
		expect(search.busy).toBe(true);
		// Nothing matches before the worker answers.
		expect(search.matcher().test(lines[1])).toBe(false);
		const w = FakeWorker.all[0];
		w.ready();
		expect(w.requests[0].texts).toEqual(['GET / 200', 'GET /login 500']);
		w.answer();
		expect(search.busy).toBe(false);
		const m = search.matcher();
		expect(lines.map((l) => m.test(l))).toEqual([false, true]);
		expect(m.ranges(lines[1])).toEqual([[11, 14]]);
		// A new line while following: only it goes to the worker.
		const more = [...lines, line(3, 'GET / 503')];
		search.run(p500, more);
		expect(w.requests[1].texts).toEqual(['GET / 503']);
		w.answer();
		expect(search.matcher().test(more[2])).toBe(true);
		// Nothing new: no request.
		search.run(p500, more);
		expect(w.requests).toHaveLength(2);
	});

	it('starts the time limit once the worker has loaded, per chunk', () => {
		const { search, timers } = setup();
		const lines = Array.from({ length: CHUNK + 5 }, (_, i) => line(i + 1, `GET /${i} 500`));
		search.run(p500, lines);
		const w = FakeWorker.all[0];
		// A cold start does not count against the pattern.
		expect(timers).toHaveLength(0);
		w.ready();
		expect(timers).toHaveLength(1);
		expect(w.requests[0].texts).toHaveLength(CHUNK);
		w.answer();
		expect(timers[0].cleared).toBe(true);
		// The rest is the next chunk, with a limit of its own.
		expect(w.requests[1].texts).toHaveLength(5);
		expect(timers).toHaveLength(2);
		w.answer();
		expect(search.busy).toBe(false);
		expect(lines.every((l) => search.matcher().test(l))).toBe(true);
	});

	it('searches lines that arrive while a chunk runs once it ends', () => {
		const { search } = setup();
		search.run(p500, [line(1, 'a 500')]);
		search.run(p500, [line(1, 'a 500'), line(2, 'b 501')]);
		const w = FakeWorker.all[0];
		w.ready();
		expect(w.requests).toHaveLength(1);
		w.answer();
		expect(w.requests[1].texts).toEqual(['b 501']);
		w.answer();
		expect(search.matcher().test(line(2, 'b 501'))).toBe(true);
	});

	it('starts over for a new pattern, stopping a chunk that may never end', () => {
		const { search, timers } = setup();
		const lines = [line(1, 'ERROR boom'), line(2, 'GET / 500')];
		search.run(p500, lines);
		const first = FakeWorker.all[0];
		first.ready();
		search.run({ source: 'error', flags: 'gi' }, lines);
		expect(first.terminated).toBe(true);
		expect(timers[0].cleared).toBe(true);
		const second = FakeWorker.all[1];
		expect(second.requests[0].texts).toEqual(['ERROR boom', 'GET / 500']);
		second.ready();
		second.answer();
		expect(lines.map((l) => search.matcher().test(l))).toEqual([true, false]);
	});

	it('stops a pattern that runs past the time limit and reports it as slow', () => {
		const { search, timers } = setup();
		const slow = { source: '(a|aa)+$', flags: 'gi' };
		search.run(slow, [line(1, 'aaaa!')]);
		const w = FakeWorker.all[0];
		w.ready();
		timers[0].fn();
		expect(w.terminated).toBe(true);
		expect(search.slow).toBe(true);
		expect(search.busy).toBe(false);
		// A late answer is ignored and the same pattern stays stopped.
		w.answer();
		expect(search.slow).toBe(true);
		search.run(slow, [line(1, 'aaaa!'), line(2, 'aa')]);
		expect(FakeWorker.all).toHaveLength(1);
		// Another pattern searches again in a new worker.
		search.run(p500, [line(1, 'x 500')]);
		expect(search.slow).toBe(false);
		expect(FakeWorker.all).toHaveLength(2);
		search.dispose();
		expect(FakeWorker.all[1].terminated).toBe(true);
	});

	it('reports a worker that fails as unavailable, never as a slow pattern', () => {
		const { search, timers } = setup();
		search.run(p500, [line(1, 'x 500')]);
		const w = FakeWorker.all[0];
		w.onerror?.(new Event('error') as ErrorEvent);
		expect(w.terminated).toBe(true);
		expect(search.failed).toBe(true);
		expect(search.slow).toBe(false);
		expect(search.busy).toBe(false);
		expect(timers).toHaveLength(0);
		// No new worker for later searches.
		search.run({ source: 'x', flags: 'gi' }, [line(1, 'x 500')]);
		expect(FakeWorker.all).toHaveLength(1);
	});
});
