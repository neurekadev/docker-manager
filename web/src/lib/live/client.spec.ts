import { describe, expect, it } from 'vitest';
import {
	LiveClient,
	LIVE_URL,
	POLL_DETAIL_MS,
	REFRESH_MS,
	type EventSourceLike,
	type QueryClientLike,
	type Scheduler
} from './client';
import { LiveStatus } from './status.svelte';
import type { QueryKey } from './keys';

class FakeSource implements EventSourceLike {
	listeners = new Map<string, ((e: MessageEvent) => void)[]>();
	onerror: ((e: Event) => void) | null = null;
	closed = false;
	constructor(public url: string) {}
	addEventListener(type: string, l: (e: MessageEvent) => void) {
		this.listeners.set(type, [...(this.listeners.get(type) ?? []), l]);
	}
	close() {
		this.closed = true;
	}
	emit(type: string, data: unknown, id = '') {
		for (const l of this.listeners.get(type) ?? []) {
			l({ data: JSON.stringify(data), lastEventId: id } as MessageEvent);
		}
	}
	fail() {
		this.onerror?.(new Event('error'));
	}
}

/** Records invalidations; `queries` are the cached keys predicates run on. */
class FakeQueryClient implements QueryClientLike {
	calls: string[] = [];
	cleared = 0;
	queries: QueryKey[] = [];
	async invalidateQueries(f?: {
		queryKey?: QueryKey;
		predicate?: (q: { queryKey: QueryKey }) => boolean;
	}) {
		if (!f) this.calls.push('*');
		else if (f.queryKey) this.calls.push(JSON.stringify(f.queryKey));
		else if (f.predicate) {
			const hit = this.queries.filter((k) => f.predicate!({ queryKey: k }));
			this.calls.push('pred:' + hit.map((k) => JSON.stringify(k)).join('|'));
		}
	}
	clear() {
		this.cleared++;
	}
	take(): string[] {
		const c = this.calls;
		this.calls = [];
		return c;
	}
}

/** Manual timers and clock. */
class FakeScheduler implements Scheduler {
	t = 1_000_000;
	timers = new Map<number, { at: number; fn: () => void }>();
	#next = 1;
	now() {
		return this.t;
	}
	random() {
		return 0.5;
	}
	setTimeout(fn: () => void, ms: number) {
		const id = this.#next++;
		this.timers.set(id, { at: this.t + ms, fn });
		return id;
	}
	clearTimeout(h: unknown) {
		this.timers.delete(h as number);
	}
	/** Advances time, firing due timers in order. */
	advance(ms: number) {
		const end = this.t + ms;
		for (;;) {
			const due = [...this.timers.entries()]
				.filter(([, v]) => v.at <= end)
				.sort((a, b) => a[1].at - b[1].at)[0];
			if (!due) break;
			this.timers.delete(due[0]);
			this.t = due[1].at;
			due[1].fn();
		}
		this.t = end;
	}
}

function setup() {
	const sources: FakeSource[] = [];
	const qc = new FakeQueryClient();
	const clock = new FakeScheduler();
	const status = new LiveStatus();
	let permissionRefetches = 0;
	const client = new LiveClient({
		queryClient: qc,
		connect: (url) => {
			const s = new FakeSource(url);
			sources.push(s);
			return s;
		},
		status,
		scheduler: clock,
		onPermissionsChanged: () => {
			permissionRefetches++;
		}
	});
	return {
		client,
		qc,
		clock,
		status,
		sources,
		last: () => sources[sources.length - 1],
		permissionRefetches: () => permissionRefetches
	};
}

const hello = (cursor: string, resumed = false) => ({
	version: 'dockyard.live/v1',
	cursor,
	heartbeatMs: 15000,
	topics: [],
	resumed
});

const container = (name: string) => ({
	topic: 'containers',
	kind: 'container',
	resourceId: name,
	environmentId: 'e1',
	action: 'updated',
	at: '2026-09-25T00:00:00Z'
});

describe('LiveClient', () => {
	it('fetches a fresh snapshot on hello and applies events after the cursor', () => {
		const { client, qc, status, last } = setup();
		client.start();
		expect(last().url).toBe(LIVE_URL);
		expect(status.state).toBe('connecting');
		last().emit('hello', hello('ep.4'));
		expect(status.state).toBe('live');
		expect(qc.take()).toEqual(['*']);
		last().emit('invalidate', container('web'), 'ep.5');
		expect(qc.take()).toEqual([
			JSON.stringify(['containers', 'list']),
			JSON.stringify(['containers', 'item', 'e1', 'web']),
			JSON.stringify(['stacks', 'services'])
		]);
		expect(client.cursor).toBe('ep.5');
	});

	it('ignores duplicate and out-of-order ids (dedupe)', () => {
		const { client, qc, last, clock } = setup();
		client.start();
		last().emit('hello', hello('ep.4'));
		qc.take();
		last().emit(
			'job',
			{ jobId: 'j1', kind: 'stack.deploy', state: 'running', revision: 3, at: '' },
			'ep.5'
		);
		last().emit(
			'job',
			{ jobId: 'j1', kind: 'stack.deploy', state: 'running', revision: 3, at: '' },
			'ep.5'
		);
		last().emit(
			'job',
			{ jobId: 'j0', kind: 'stack.deploy', state: 'running', revision: 1, at: '' },
			'ep.3'
		);
		expect(qc.take()).toEqual([
			JSON.stringify(['jobs', 'item', 'j1']),
			JSON.stringify(['jobs', 'list'])
		]);
		clock.advance(REFRESH_MS.list);
		expect(qc.take()).toEqual([]);
	});

	it('resumes from its cursor after a disconnect without refetching everything', () => {
		const { client, qc, clock, sources, last, status } = setup();
		client.start();
		last().emit('hello', hello('ep.1'));
		last().emit('invalidate', container('web'), 'ep.2');
		qc.take();
		last().fail();
		expect(sources[0].closed).toBe(true);
		expect(status.state).toBe('reconnecting');
		clock.advance(1_000);
		expect(sources).toHaveLength(2);
		expect(last().url).toBe(`${LIVE_URL}?cursor=ep.2`);
		last().emit('hello', hello('ep.9', true));
		expect(qc.take()).toEqual([]); // cached data stays valid
		last().emit('invalidate', container('db'), 'ep.3'); // replayed
		expect(client.cursor).toBe('ep.3');
		expect(status.state).toBe('live');
	});

	it('refetches after a gap or an expired cursor, one environment when scoped', () => {
		const { client, qc, last } = setup();
		qc.queries = [
			['environments', 'item', 'e1'],
			['environments', 'item', 'e2'],
			['containers', 'item', 'e1', 'web'],
			['containers', 'item', 'e2', 'web'],
			['containers', 'list'],
			['jobs', 'item', 'j1'],
			['files', 'volume', 'e1/data', 'list', '.'],
			['files', 'volume', 'e2/data', 'list', '.']
		];
		client.start();
		last().emit('hello', hello('ep.1'));
		qc.take();
		last().emit('reset', { reason: 'gap', cursor: 'ep.7', environmentId: 'e1' });
		expect(qc.take()).toEqual([
			'pred:' +
				[
					['environments', 'item', 'e1'],
					['containers', 'item', 'e1', 'web'],
					['containers', 'list'],
					['files', 'volume', 'e1/data', 'list', '.']
				]
					.map((k) => JSON.stringify(k))
					.join('|')
		]);
		expect(client.cursor).toBe('ep.7');
		last().emit('reset', { reason: 'cursor_expired', cursor: 'ep2.0' });
		expect(qc.take()).toEqual(['*']);
		expect(client.cursor).toBe('ep2.0');
	});

	it('clears all cached data at once when permissions change, then reconnects fresh', async () => {
		const { client, qc, last, sources, permissionRefetches } = setup();
		client.start();
		last().emit('hello', hello('ep.1'));
		last().emit('invalidate', container('web'), 'ep.2');
		last().emit('permissions.changed', { at: '' });
		await Promise.resolve();
		expect(qc.cleared).toBe(1);
		expect(permissionRefetches()).toBe(1);
		expect(client.cursor).toBeNull();
		last().emit('close', { reason: 'permissions_changed' });
		expect(sources).toHaveLength(2);
		expect(last().url).toBe(LIVE_URL); // no cursor: a fresh, re-filtered snapshot
	});

	it('stops on session_expired until reconnectNow', () => {
		const { client, clock, last, sources, status } = setup();
		client.start();
		last().emit('hello', hello('ep.1'));
		last().emit('close', { reason: 'session_expired' });
		expect(status.state).toBe('unauthenticated');
		clock.advance(60_000);
		expect(sources).toHaveLength(1);
		client.reconnectNow();
		expect(sources).toHaveLength(2);
	});

	it('reconnects at once after max_age, keeping the cursor', () => {
		const { client, last, sources } = setup();
		client.start();
		last().emit('hello', hello('ep.1'));
		last().emit('invalidate', container('web'), 'ep.2');
		last().emit('close', { reason: 'max_age' });
		expect(sources).toHaveLength(2);
		expect(last().url).toBe(`${LIVE_URL}?cursor=ep.2`);
	});

	it('falls back to bounded polling after three failures within a minute', () => {
		const { client, qc, clock, last, status } = setup();
		qc.queries = [
			['stacks', 'item', 's1'],
			['stacks', 'list'],
			['metrics', 'item', 'e1']
		];
		client.start();
		for (let i = 0; i < 3; i++) {
			last().fail();
			if (i < 2) clock.advance(5_000); // the reconnect attempt
		}
		expect(status.state).toBe('polling');
		expect(client.polling).toBe(true);
		const connections = () => qc.take();
		connections();
		// Details every 10 s ...
		clock.advance(POLL_DETAIL_MS);
		const first = qc.calls.filter((c) => c.startsWith('pred:'));
		expect(first).toEqual([
			'pred:' +
				JSON.stringify(['stacks', 'item', 's1']) +
				'|' +
				JSON.stringify(['metrics', 'item', 'e1'])
		]);
		qc.take();
		// ... lists and metrics every 30 s.
		clock.advance(POLL_DETAIL_MS * 2);
		expect(qc.calls.filter((c) => c.startsWith('pred:')).pop()).toContain(
			JSON.stringify(['stacks', 'list'])
		);
		// The stream is back: polling stops.
		clock.advance(30_000);
		last().emit('hello', hello('ep.1'));
		expect(client.polling).toBe(false);
		expect(status.state).toBe('live');
	});

	it('refreshes lists at most every second however many events arrive', () => {
		const { client, qc, clock, last } = setup();
		client.start();
		last().emit('hello', hello('ep.0'));
		qc.take();
		for (let i = 1; i <= 50; i++) last().emit('invalidate', container('c' + i), 'ep.' + i);
		const lists = () => qc.take().filter((c) => c === JSON.stringify(['containers', 'list']));
		expect(lists()).toHaveLength(1);
		clock.advance(REFRESH_MS.list);
		expect(lists()).toHaveLength(1);
		clock.advance(REFRESH_MS.list * 5);
		expect(lists()).toHaveLength(0);
	});

	it('reconnects from the cursor when the open views change', () => {
		const { client, last, sources } = setup();
		client.start();
		last().emit('hello', hello('ep.1'));
		last().emit('invalidate', container('web'), 'ep.2');
		client.setScopes({ volumes: ['e1/pgdata'], stackIds: ['s1'] });
		expect(sources[0].closed).toBe(true);
		expect(last().url).toBe(`${LIVE_URL}?stackId=s1&volume=e1%2Fpgdata&cursor=ep.2`);
		client.setScopes({ stackIds: ['s1'], volumes: ['e1/pgdata'] }); // unchanged
		expect(sources).toHaveLength(2);
	});

	it('tracks environment connection state from agent events', () => {
		const { client, last, status } = setup();
		client.start();
		last().emit('hello', hello('ep.1'));
		last().emit('agent', { environmentId: 'e1', status: 'offline', at: '' }, 'ep.2');
		expect(status.environments).toEqual({ e1: 'offline' });
		client.stop();
		expect(status.state).toBe('stopped');
	});
});
