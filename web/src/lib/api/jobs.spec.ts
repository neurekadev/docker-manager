import { describe, expect, it, vi } from 'vitest';
import { demoJob, demoJobClient, demoJobEvents, ScriptedEventSource } from '$lib/design/demo';
import type { ApiClient, Job } from './client';
import { isTerminal, JobWatcher } from './jobs.svelte';

const flush = () => new Promise((r) => setTimeout(r, 0));

describe('JobWatcher (#26 job event stream)', () => {
	it('follows a job to a partial failure and reports it once', async () => {
		const es = new ScriptedEventSource();
		const urls: string[] = [];
		const finished = vi.fn();
		const w = new JobWatcher('job/1', {
			eventSource: (url) => (urls.push(url), es),
			client: demoJobClient(demoJob('partial')),
			onfinish: finished
		});
		w.start();
		expect(urls).toEqual(['/api/v1/jobs/job%2F1/events/stream']);
		es.emit('job', demoJob('running'));
		expect(w.state).toBe('running');
		expect(w.terminal).toBe(false);

		for (const ev of demoJobEvents.slice(0, 3)) es.emit(ev.type, ev);
		expect(w.job?.progress.percent).toBe(35);
		expect(w.items.map((i) => `${i.name}:${i.status}`)).toEqual([
			'tmp-builder:succeeded',
			'old-nextcloud:failed'
		]);
		expect(w.failedItems).toHaveLength(1);

		// A replayed event (reconnect with Last-Event-ID) is ignored.
		es.emit('item', demoJobEvents[1]);
		expect(w.items).toHaveLength(2);

		for (const ev of demoJobEvents.slice(3)) es.emit(ev.type, ev);
		await flush();
		expect(w.state).toBe('partial');
		expect(w.terminal).toBe(true);
		expect(w.job?.error?.recovery).toMatch(/run the job again/);
		expect(es.closed).toBe(true);
		expect(finished).toHaveBeenCalledTimes(1);
		expect((finished.mock.calls[0][0] as Job).state).toBe('partial');
		es.emit('close', { reason: 'max_age' });
		expect(finished).toHaveBeenCalledTimes(1);
	});

	it('falls back to polling when the stream cannot be opened', async () => {
		vi.useFakeTimers();
		try {
			const es = new ScriptedEventSource();
			let n = 0;
			const client = {
				GET: async () => {
					n++;
					return {
						data: demoJob(n < 2 ? 'running' : 'succeeded'),
						response: new Response('{}')
					};
				}
			} as unknown as ApiClient;
			const done = vi.fn();
			const w = new JobWatcher('j', {
				eventSource: () => es,
				client,
				pollMs: 1000,
				onfinish: done
			});
			w.start();
			es.fail();
			await vi.advanceTimersByTimeAsync(0);
			expect(n).toBe(1);
			expect(w.state).toBe('running');
			await vi.advanceTimersByTimeAsync(1000);
			expect(n).toBe(2);
			expect(w.state).toBe('succeeded');
			expect(done).toHaveBeenCalledTimes(1);
			await vi.advanceTimersByTimeAsync(5000);
			expect(n).toBe(2); // stops polling once terminal
			w.stop();
		} finally {
			vi.useRealTimers();
		}
	});

	it('knows the terminal states', () => {
		for (const s of ['succeeded', 'failed', 'partial', 'cancelled', 'interrupted'])
			expect(isTerminal(s)).toBe(true);
		for (const s of ['queued', 'blocked', 'dispatched', 'running', 'cancelling', undefined])
			expect(isTerminal(s)).toBe(false);
	});
});
