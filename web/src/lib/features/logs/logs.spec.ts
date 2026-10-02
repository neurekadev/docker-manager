// Log feed (#8): SSE per container, merge by time, cursor resume with
// dedupe, dropped counts, end reasons, follow on/off, bounded buffer;
// levels, search and the viewer's filters.
import { describe, expect, it } from 'vitest';
import { LogFeed, type EventSourceLike, type LogLine, type RawLine } from './feed.svelte';
import {
	filterLines,
	formatLogTime,
	hiddenServices,
	highlight,
	levelSummary,
	logText,
	plainSearch,
	regexMatches,
	regexPattern,
	serviceShown,
	serviceSummary,
	serviceTally,
	tally,
	type Matcher
} from './format';
import { continues, detectLevel, stripAnsi, type LogLevel } from './level';
import { endReason, streamUrl, timeKey, type LogSource } from './stream';

/** A plain search that is not empty (fails the test otherwise). */
function plain(query: string, caseSensitive = false): Matcher {
	const m = plainSearch(query, caseSensitive);
	if (!m) throw new Error(`search ${query} is empty`);
	return m;
}

/** A line with only its text (the search reads nothing else). */
function textLine(text: string): LogLine {
	return { seq: 0, source: 'k', at: 'T', key: 'T', stream: 'stdout', text, level: 'other' };
}

class FakeES implements EventSourceLike {
	static all: FakeES[] = [];
	readyState = 0;
	onerror: ((ev: Event) => void) | null = null;
	onopen: ((ev: Event) => void) | null = null;
	closed = false;
	#listeners = new Map<string, ((ev: MessageEvent) => void)[]>();

	constructor(readonly url: string) {
		FakeES.all.push(this);
	}
	addEventListener(type: string, l: (ev: MessageEvent) => void) {
		this.#listeners.set(type, [...(this.#listeners.get(type) ?? []), l]);
	}
	close() {
		this.closed = true;
		this.readyState = 2;
	}
	emit(type: string, data: unknown, id = '') {
		for (const l of this.#listeners.get(type) ?? [])
			l({ data: JSON.stringify(data), lastEventId: id } as MessageEvent);
	}
	line(at: string, line: string, stream = 'stdout') {
		this.emit('log', { at, stream, line }, at);
	}
	fail() {
		this.readyState = 2;
		this.onerror?.(new Event('error'));
	}
}

const web: LogSource = {
	key: 'silo-web-1',
	environmentId: 'e1',
	containerId: 'silo-web-1',
	service: 'silo-web',
	label: 'silo-web'
};
const db: LogSource = {
	key: 'silo-db-1',
	environmentId: 'e1',
	containerId: 'silo-db-1',
	service: 'silo-db',
	label: 'silo-db'
};

function feed(sources: LogSource[], extra: Record<string, unknown> = {}) {
	FakeES.all = [];
	return new LogFeed(sources, { eventSource: (u) => new FakeES(u), tail: 100, ...extra });
}

describe('stream helpers', () => {
	it('orders RFC 3339 timestamps with trimmed fractions', () => {
		expect(timeKey('2026-09-25T10:00:00Z') < timeKey('2026-09-25T10:00:00.5Z')).toBe(true);
		expect(timeKey('2026-09-25T10:00:00.1Z') > timeKey('2026-09-25T10:00:00.05Z')).toBe(true);
		expect(timeKey('2026-09-25T12:00:00+02:00')).toBe('2026-09-25T10:00:00.000000000Z');
		expect(timeKey('garbage')).toBe('garbage');
	});

	it('builds the stream URL: tail first, since when resuming', () => {
		expect(streamUrl(web, 200)).toBe(
			'/api/v1/environments/e1/containers/silo-web-1/logs/stream?tail=200'
		);
		expect(streamUrl(web, 200, '2026-09-25T10:00:00.5Z')).toBe(
			'/api/v1/environments/e1/containers/silo-web-1/logs/stream?since=2026-09-25T10%3A00%3A00.5Z'
		);
		expect(endReason('container_removed')).toMatch(/removed/);
		expect(endReason('agent_offline')).toMatch(/offline/);
	});

	it('formats, highlights and exports lines', () => {
		expect(highlight('GET /health 200', [[5, 11]])).toEqual([
			{ text: 'GET /', match: false },
			{ text: 'health', match: true },
			{ text: ' 200', match: false }
		]);
		expect(highlight('abc', [])).toEqual([{ text: 'abc', match: false }]);
		expect(formatLogTime('nope')).toBe('nope');
		expect(formatLogTime('2026-09-25T10:14:22Z')).toMatch(/^2026-09-2\d \d\d:14:22$/);
		const text = logText(
			[
				{
					seq: 1,
					source: 'k',
					at: 'T1',
					key: 'T1',
					stream: 'stderr',
					text: 'boom',
					level: 'other'
				}
			],
			{ timestamps: true, source: () => 'silo-web' }
		);
		expect(text).toBe('T1 silo-web stderr boom\n');
	});
});

describe('levels', () => {
	it.each<[string, LogLevel]>([
		['time="2026-09-25T10:00:00Z" level=warning msg="slow request"', 'warning'],
		['{"level":"debug","msg":"loaded config"}', 'debug'],
		['{"severity":"ERROR","message":"boom"}', 'error'],
		['{"level":30,"msg":"listening"}', 'info'],
		['{"level":50,"msg":"failed"}', 'error'],
		['{"level":10,"msg":"entering"}', 'verbose'],
		['2026/09/25 10:00:00 [error] 29#29: *1 open() failed', 'error'],
		['2026/09/25 10:00:00 [notice] 1#1: start worker processes', 'info'],
		['2026-09-25 10:00:00.000 UTC [44] ERROR:  relation "users" does not exist', 'error'],
		['2026-09-25 10:00:00.000 UTC [1] LOG:  database system is ready', 'info'],
		['2026-09-25 10:00:00.000 UTC [1] DEBUG2:  autovacuum', 'debug'],
		['2026-09-25T10:00:00Z WRN retrying in 5s', 'warning'],
		['10:00:00 TRACE entering handler', 'verbose'],
		['I0925 10:14:22.123456       1 server.go:42] serving', 'info'],
		['E0925 10:14:22.123456       1 server.go:42] failed', 'error'],
		['npm ERR! code ELIFECYCLE', 'error'],
		['panic: runtime error: index out of range', 'error'],
		['warning: unused variable', 'warning'],
		['ValueError: invalid literal for int()', 'error'],
		['Traceback (most recent call last):', 'error'],
		// The first level word wins; lower-case words in messages are no level.
		['INFO retry after ERROR in upstream', 'info'],
		['GET /errors 200 1.2ms', 'other'],
		['found 0 errors and 1 warning', 'other'],
		['ERRORS_TOTAL=0', 'other'],
		['', 'other']
	])('reads %j as %s', (text, level) => {
		expect(detectLevel(text)).toBe(level);
	});

	it('marks continuation lines and strips terminal colours', () => {
		expect(continues('    at com.example.Pool.acquire(Pool.java:42)')).toBe(true);
		expect(continues('\tmain.go:12 +0x1d')).toBe(true);
		expect(continues('Caused by: java.io.IOException')).toBe(true);
		expect(continues('GET / 200')).toBe(false);
		expect(continues('   ')).toBe(false);
		expect(stripAnsi('\x1b[32mINFO\x1b[0m ready')).toBe('INFO ready');
		expect(stripAnsi('\x1b]0;title\x07plain')).toBe('plain');
		expect(stripAnsi('no escapes')).toBe('no escapes');
	});
});

describe('search', () => {
	it('matches plain text case-insensitively, trimmed, unless Match Case', () => {
		expect(plainSearch('')).toBeNull();
		expect(plainSearch('   ')).toBeNull();
		const m = plain(' get ');
		expect(m.test(textLine('GET /health'))).toBe(true);
		expect(m.ranges(textLine('get GET'))).toEqual([
			[0, 3],
			[4, 7]
		]);
		const exact = plain('GET', true);
		expect(exact.test(textLine('get /health'))).toBe(false);
		expect(exact.ranges(textLine('get GET'))).toEqual([[4, 7]]);
	});

	it('compiles regular expressions and reports invalid ones', () => {
		expect(regexPattern('')).toBeNull();
		expect(regexPattern('(oops')).toBe('invalid');
		expect(regexPattern(String.raw`5\d\d`)).toEqual({ source: String.raw`5\d\d`, flags: 'gi' });
		expect(regexPattern('ERROR', true)).toEqual({ source: 'ERROR', flags: 'g' });
	});

	it('matches regular expressions line by line (the worker side)', () => {
		const p = { source: String.raw`5\d\d`, flags: 'gi' };
		expect(regexMatches(p, ['GET /login 500', 'GET /login 200', '500 then 502'])).toEqual([
			[[11, 14]],
			null,
			[
				[0, 3],
				[9, 12]
			]
		]);
		expect(regexMatches({ source: 'error', flags: 'gi' }, ['ERROR'])).toEqual([[[0, 5]]]);
		expect(regexMatches({ source: 'error', flags: 'g' }, ['ERROR'])).toEqual([null]);
		// Only empty matches: the line matches, nothing is highlighted.
		expect(regexMatches({ source: 'x*', flags: 'gi' }, ['abc'])).toEqual([[]]);
		expect(highlight('abc', [])).toEqual([{ text: 'abc', match: false }]);
	});
});

describe('line filters', () => {
	const line = (
		seq: number,
		source: string,
		text: string,
		stream: 'stdout' | 'stderr' = 'stdout',
		level: LogLevel = 'other'
	): LogLine => ({
		seq,
		source,
		at: `T${seq}`,
		key: `T${seq}`,
		stream,
		text,
		level
	});
	const lines = [
		line(1, 'web', 'GET /health 200', 'stdout', 'info'),
		line(2, 'db', 'connection refused', 'stderr', 'error'),
		line(3, 'web', 'GET /login 500', 'stderr', 'warning'),
		line(4, 'db', 'checkpoint complete')
	];

	it('returns the lines unchanged when nothing filters', () => {
		expect(filterLines(lines, {})).toBe(lines);
		expect(filterLines(lines, { match: null })).toBe(lines);
		// Every level and both streams chosen: nothing to filter.
		expect(
			filterLines(lines, {
				levels: ['error', 'warning', 'info', 'debug', 'verbose', 'other'],
				streams: ['stdout', 'stderr']
			})
		).toBe(lines);
	});

	it('keeps the chosen levels, streams, matches and services, combined', () => {
		expect(filterLines(lines, { levels: ['error', 'warning'] }).map((l) => l.seq)).toEqual([
			2, 3
		]);
		expect(filterLines(lines, { streams: ['stderr'] }).map((l) => l.seq)).toEqual([2, 3]);
		expect(filterLines(lines, { match: plain('get') }).map((l) => l.seq)).toEqual([1, 3]);
		expect(filterLines(lines, { source: (k) => k === 'db' }).map((l) => l.seq)).toEqual([2, 4]);
		expect(
			filterLines(lines, {
				source: (k) => k === 'web',
				levels: ['warning'],
				streams: ['stderr'],
				match: plain('login')
			}).map((l) => l.seq)
		).toEqual([3]);
		expect(filterLines(lines, { match: plain('nothing') })).toEqual([]);
		expect(filterLines(lines, { levels: [] })).toEqual([]);
	});

	it('counts levels and streams and names the choice', () => {
		expect(tally(lines)).toEqual({
			error: 1,
			warning: 1,
			info: 1,
			debug: 0,
			verbose: 0,
			other: 1,
			stdout: 2,
			stderr: 2
		});
		const every: LogLevel[] = ['error', 'warning', 'info', 'debug', 'verbose', 'other'];
		expect(levelSummary(every, ['stdout', 'stderr'])).toBe('All Levels');
		expect(levelSummary(['warning', 'error'], ['stdout', 'stderr'])).toBe('Error, Warning');
		expect(levelSummary(['error', 'warning', 'info'], ['stdout', 'stderr'])).toBe('3 levels');
		expect(levelSummary(every, ['stderr'])).toBe('All Levels · stderr');
		expect(levelSummary([], ['stdout'])).toBe('Nothing Selected');
		expect(levelSummary(['error'], [])).toBe('Nothing Selected');
	});

	it('starts with one service from ?service= and ignores an unknown one', () => {
		const services = ['silo-web', 'silo-db', 'silo-cache'];
		const focused = { only: 'silo-db', hidden: [], services };
		expect(services.map((s) => serviceShown(s, focused))).toEqual([false, true, false]);
		const unknown = { only: 'gone', hidden: [], services };
		expect(services.map((s) => serviceShown(s, unknown))).toEqual([true, true, true]);
		const some = { only: null, hidden: ['silo-web'], services };
		expect(services.map((s) => serviceShown(s, some))).toEqual([false, true, true]);
	});

	it('hides the services the Services filter leaves out and names the choice', () => {
		const services = ['silo-web', 'silo-db', 'silo-cache'];
		expect(hiddenServices(services, ['silo-db'])).toEqual(['silo-web', 'silo-cache']);
		expect(hiddenServices(services, services)).toEqual([]);
		expect(serviceSummary(services, services)).toBe('All Services');
		expect(serviceSummary(services, ['silo-cache', 'silo-web'])).toBe('silo-web, silo-cache');
		expect(serviceSummary([...services, 'silo-worker'], services)).toBe('3 services');
		expect(serviceSummary(services, [])).toBe('Nothing Selected');
	});

	it('counts the lines of each service', () => {
		const serviceOf = { web: 'silo-web', db: 'silo-db' };
		expect(serviceTally(lines, serviceOf)).toEqual({ 'silo-web': 2, 'silo-db': 2 });
		expect(serviceTally(filterLines(lines, { levels: ['error'] }), serviceOf)).toEqual({
			'silo-db': 1
		});
		// A line whose source left the stack counts for no service.
		expect(serviceTally(lines, { web: 'silo-web' })).toEqual({ 'silo-web': 2 });
	});
});

describe('LogFeed', () => {
	it('merges containers by time and marks sources live', () => {
		const f = feed([web, db]);
		f.start();
		expect(FakeES.all.map((e) => e.url)).toEqual([
			'/api/v1/environments/e1/containers/silo-web-1/logs/stream?tail=100',
			'/api/v1/environments/e1/containers/silo-db-1/logs/stream?tail=100'
		]);
		const [w, d] = FakeES.all;
		w.line('2026-09-25T10:00:01Z', 'web one');
		w.line('2026-09-25T10:00:03Z', 'web two');
		d.line('2026-09-25T10:00:02Z', 'db one');
		d.line('2026-09-25T10:00:02.5Z', 'db two', 'stderr');
		expect(f.lines.map((l) => l.text)).toEqual(['web one', 'db one', 'db two', 'web two']);
		expect(f.lines[2].stream).toBe('stderr');
		expect(f.states[web.key]).toEqual({ kind: 'live' });
		d.emit('dropped', { count: 7 });
		expect(f.dropped).toBe(7);
		f.stop();
		expect(FakeES.all.every((e) => e.closed)).toBe(true);
	});

	it('strips colours and gives each line its level; stack frames keep their container’s', () => {
		const f = feed([web, db]);
		f.start();
		const [w, d] = FakeES.all;
		w.line('2026-09-25T10:00:01Z', '\x1b[31mERROR\x1b[0m pool exhausted', 'stderr');
		d.line('2026-09-25T10:00:02Z', 'LOG:  checkpoint starting', 'stderr');
		w.line('2026-09-25T10:00:03Z', '    at Pool.acquire(Pool.java:42)', 'stderr');
		d.line('2026-09-25T10:00:04Z', '    detail of the checkpoint');
		w.line('2026-09-25T10:00:05Z', 'GET / 200');
		w.line('2026-09-25T10:00:06Z', '    indented after a line without a level');
		expect(f.lines.map((l) => [l.text, l.level])).toEqual([
			['ERROR pool exhausted', 'error'],
			['LOG:  checkpoint starting', 'info'],
			['    at Pool.acquire(Pool.java:42)', 'error'],
			['    detail of the checkpoint', 'info'],
			['GET / 200', 'other'],
			['    indented after a line without a level', 'other']
		]);
		f.stop();
	});

	it('resumes at the cursor after Follow is turned back on and skips repeats', () => {
		const f = feed([web]);
		f.start();
		const first = FakeES.all[0];
		first.line('2026-09-25T10:00:01Z', 'a');
		first.line('2026-09-25T10:00:02Z', 'b1');
		first.line('2026-09-25T10:00:02Z', 'b2');
		f.setFollowing(false);
		expect(first.closed).toBe(true);
		expect(f.states[web.key]).toEqual({ kind: 'paused' });
		f.setFollowing(true);
		const resumed = FakeES.all[1];
		expect(resumed.url).toBe(
			'/api/v1/environments/e1/containers/silo-web-1/logs/stream?since=2026-09-25T10%3A00%3A02Z'
		);
		// The resume replays lines of the cursor's second: only new ones count.
		resumed.line('2026-09-25T10:00:02Z', 'b1');
		resumed.line('2026-09-25T10:00:02Z', 'b2');
		resumed.line('2026-09-25T10:00:02Z', 'b3');
		resumed.line('2026-09-25T10:00:01Z', 'old');
		resumed.line('2026-09-25T10:00:04Z', 'c');
		expect(f.lines.map((l) => l.text)).toEqual(['a', 'b1', 'b2', 'b3', 'c']);
	});

	it('ends on end events, retries offline environments and classifies failures', async () => {
		const timers: (() => void)[] = [];
		const f = feed([web, db], {
			retryMs: 5000,
			setTimer: (fn: () => void) => timers.push(fn),
			clearTimer: () => {},
			probe: async () => ({ status: 403, message: 'You cannot read these logs.' })
		});
		f.start();
		const [w, d] = FakeES.all;
		w.emit('end', { reason: 'agent_offline' });
		expect(f.states[web.key]).toEqual({ kind: 'ended', reason: 'agent_offline' });
		expect(timers).toHaveLength(1);
		timers[0]();
		expect(FakeES.all).toHaveLength(3);
		d.fail();
		await Promise.resolve();
		await Promise.resolve();
		expect(f.states[db.key]).toEqual({
			kind: 'failed',
			status: 403,
			message: 'You cannot read these logs.'
		});
		// Denied is final: no retry is scheduled for it.
		expect(timers).toHaveLength(1);
		f.retry(db.key);
		expect(FakeES.all).toHaveLength(4);
		f.stop();
	});

	it('polls containers beyond the stream budget with the same cursor and dedupe', async () => {
		const timers: (() => void)[] = [];
		const calls: (string | undefined)[] = [];
		const answers: (RawLine[] | Error)[] = [
			[{ at: '2026-09-25T10:00:01Z', stream: 'stdout', line: 'p1' }],
			[
				{ at: '2026-09-25T10:00:01Z', stream: 'stdout', line: 'p1' },
				{ at: '2026-09-25T10:00:02Z', stream: 'stderr', line: 'p2' }
			],
			Object.assign(new Error('You cannot read these logs.'), { status: 403 })
		];
		const f = feed([web, db], {
			maxStreams: 1,
			pollMs: 1000,
			setTimer: (fn: () => void) => timers.push(fn),
			clearTimer: () => {},
			poll: async (_s: LogSource, since?: string) => {
				calls.push(since);
				const a = answers.shift() ?? [];
				if (a instanceof Error) throw a;
				return a;
			}
		});
		const flush = async () => {
			for (let i = 0; i < 5; i++) await Promise.resolve();
		};
		f.start();
		expect(FakeES.all).toHaveLength(1); // web streams, db is polled
		expect(f.polled(db.key)).toBe(true);
		expect(f.polled(web.key)).toBe(false);
		await flush();
		expect(f.lines.map((l) => l.text)).toEqual(['p1']);
		expect(f.states[db.key]).toEqual({ kind: 'live' });
		timers.shift()!();
		await flush();
		expect(calls).toEqual([undefined, '2026-09-25T10:00:01Z']);
		expect(f.lines.map((l) => l.text)).toEqual(['p1', 'p2']);
		timers.shift()!();
		await flush();
		expect(f.states[db.key]).toEqual({
			kind: 'failed',
			status: 403,
			message: 'You cannot read these logs.'
		});
		expect(f.polled(db.key)).toBe(false);
		expect(timers).toHaveLength(0); // refusals are final
		f.setFollowing(false);
		f.setFollowing(true);
		expect(f.polled(db.key)).toBe(true);
		f.stop();
		expect(f.polled(db.key)).toBe(false);
	});

	it('bounds the buffer, clears the view and follows source changes', () => {
		const f = feed([web], { max: 3 });
		f.start();
		const w = FakeES.all[0];
		for (let i = 1; i <= 5; i++) w.line(`2026-09-25T10:00:0${i}Z`, `l${i}`);
		expect(f.lines.map((l) => l.text)).toEqual(['l3', 'l4', 'l5']);
		expect(f.trimmed).toBe(2);
		f.clear();
		expect(f.lines).toEqual([]);
		w.line('2026-09-25T10:00:09Z', 'after clear');
		expect(f.lines.map((l) => l.text)).toEqual(['after clear']);
		f.setSources([db]);
		expect(w.closed).toBe(true);
		expect(FakeES.all.at(-1)?.url).toContain('silo-db-1');
		expect(f.sources).toEqual([db]);
		f.stop();
	});
});
