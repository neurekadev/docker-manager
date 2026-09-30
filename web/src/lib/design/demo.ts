// Sample data for the design system gallery (/design) and component tests:
// the mockup's stack "Silo" as Docker Manager would describe it. Not API data.
import type { ApiClient, Job, JobEvent } from '$lib/api/client';
import type { EventSourceLike } from '$lib/api/jobs.svelte';

export interface DemoService {
	name: string;
	description: string;
	image: string;
	status: string;
	running: number;
	desired: number;
	ports: string[];
	restart: string;
	cpu: number;
	memory: number;
}

export const DEMO_STACK_ID = '0190a6e0-5110-7000-8000-000000000001';

export const demoServices: DemoService[] = [
	{
		name: 'silo-web',
		description: 'Web frontend',
		image: 'ghcr.io/silo/web:latest',
		status: 'running',
		running: 1,
		desired: 1,
		ports: ['8080:80'],
		restart: 'unless-stopped',
		cpu: 2.4,
		memory: 312 * 2 ** 20
	},
	{
		name: 'silo-api',
		description: 'Backend API',
		image: 'ghcr.io/silo/api:latest',
		status: 'running',
		running: 1,
		desired: 1,
		ports: ['8081:8080'],
		restart: 'unless-stopped',
		cpu: 3.1,
		memory: 420 * 2 ** 20
	},
	{
		name: 'silo-db',
		description: 'PostgreSQL',
		image: 'postgres:16',
		status: 'running',
		running: 1,
		desired: 1,
		ports: ['5432:5432'],
		restart: 'unless-stopped',
		cpu: 1.2,
		memory: 512 * 2 ** 20
	},
	{
		name: 'silo-redis',
		description: 'Redis cache',
		image: 'redis:7-alpine',
		status: 'running',
		running: 1,
		desired: 1,
		ports: ['6379:6379'],
		restart: 'unless-stopped',
		cpu: 0.7,
		memory: 128 * 2 ** 20
	},
	{
		name: 'silo-worker',
		description: 'Background worker',
		image: 'ghcr.io/silo/worker:latest',
		status: 'restarting',
		running: 0,
		desired: 1,
		ports: [],
		restart: 'unless-stopped',
		cpu: 0,
		memory: 0
	}
];

/** Memory of the demo containers of MultiSeriesChart (MB at rest). */
const demoContainerSizes: [string, number][] = [
	['vrising', 3400],
	['infinidysk', 1600],
	['silo-postgres', 1000],
	['pangolin', 760],
	['kodus-api', 600],
	['kodus-worker', 560],
	['rclone-manager', 520],
	['silo', 435],
	['uptime-kuma', 375],
	['universe-server', 330],
	['meilisearch', 290],
	['sonarr', 240],
	['radarr', 220],
	['kodus-webhooks', 180],
	['mongodb', 160],
	['prowlarr', 140],
	['redis', 60],
	['traefik', 45],
	['docker-agent', 30],
	['docker-manager', 90]
];

/**
 * Deterministic memory series (bytes) of about twenty containers for the
 * MultiSeriesChart gallery: steady use with small wobbles, one container
 * started late (leading gaps).
 */
export function demoContainerMemory(n = 60): { name: string; values: (number | null)[] }[] {
	return demoContainerSizes.map(([name, mb], k) => ({
		name,
		values: Array.from({ length: n }, (_, i) =>
			name === 'redis' && i < 12
				? null
				: Math.round((mb + mb * 0.03 * Math.sin((i + k * 3) / 5)) * 1024 * 1024)
		)
	}));
}

/** A deterministic CPU series (percent) with one gap. */
export function demoCpuSeries(n = 40): (number | null)[] {
	return Array.from({ length: n }, (_, i) =>
		i === 27 ? null : +(10 + 3 * Math.sin(i / 4) + (i % 7) * 0.3).toFixed(2)
	);
}

const at = '2026-09-25T10:14:22Z';

export function demoJob(state: Job['state'] = 'running'): Job {
	return {
		id: '0190a6e0-7000-7000-8000-000000000042',
		kind: 'prune.run',
		state,
		executor: 'agent',
		origin: 'manual',
		attempt: 1,
		cancelRequested: false,
		cancellable: state === 'running',
		createdAt: at,
		updatedAt: at,
		environmentId: 'env-nas',
		items: [],
		locks: [],
		locksHeld: state === 'running',
		progress: { percent: state === 'running' ? 20 : 100, step: 'remove' },
		targets: [{ type: 'maintenance_policy', id: 'pol-1' }],
		...(state === 'partial'
			? {
					finishedAt: at,
					error: {
						class: 'step_failed',
						message: 'Some items failed.',
						recovery: 'Review the failed items and run the job again for them.'
					}
				}
			: {})
	} as Job;
}

/** Job events of a prune run where one removal fails (partial). */
export const demoJobEvents: JobEvent[] = [
	{
		seq: 1,
		type: 'progress',
		at,
		percent: 35,
		step: 'remove',
		message: 'Removing 3 stopped containers'
	},
	{ seq: 2, type: 'item', at, item: { name: 'tmp-builder', status: 'succeeded' } },
	{
		seq: 3,
		type: 'item',
		at,
		item: {
			name: 'old-nextcloud',
			status: 'failed',
			message: 'driver "overlay2" failed to remove root filesystem: device or resource busy'
		}
	},
	{ seq: 4, type: 'progress', at, percent: 90, step: 'remove' },
	{ seq: 5, type: 'item', at, item: { name: 'speedtest', status: 'succeeded' } },
	{ seq: 6, type: 'state', at, state: 'partial' }
];

/**
 * A scripted EventSource: emits the initial `job` event, then `events`
 * one by one every `intervalMs` (tests pass 0 and drive it by hand).
 */
export class ScriptedEventSource implements EventSourceLike {
	readyState = 0;
	onerror: ((ev: Event) => void) | null = null;
	#listeners = new Map<string, ((ev: MessageEvent) => void)[]>();
	closed = false;

	addEventListener(type: string, l: (ev: MessageEvent) => void) {
		this.#listeners.set(type, [...(this.#listeners.get(type) ?? []), l]);
	}

	emit(type: string, data: unknown) {
		this.readyState = 1;
		for (const l of this.#listeners.get(type) ?? [])
			l(new MessageEvent(type, { data: JSON.stringify(data) }));
	}

	fail() {
		this.readyState = 2;
		this.onerror?.(new Event('error'));
	}

	close() {
		this.readyState = 2;
		this.closed = true;
	}
}

/** Replays the demo job with a timer (gallery only). */
export function playDemoJob(es: ScriptedEventSource, intervalMs = 900): () => void {
	const timers: ReturnType<typeof setTimeout>[] = [];
	timers.push(setTimeout(() => es.emit('job', demoJob('running')), 50));
	demoJobEvents.forEach((ev, i) =>
		timers.push(setTimeout(() => es.emit(ev.type, ev), 100 + (i + 1) * intervalMs))
	);
	return () => timers.forEach(clearTimeout);
}

/** An API client whose GET /jobs/{id} answers the finished demo job. */
export function demoJobClient(final: Job): ApiClient {
	return {
		GET: async () => ({
			data: { ...final, items: demoJobEvents.flatMap((e) => (e.item ? [e.item] : [])) },
			response: new Response('{}')
		})
	} as unknown as ApiClient;
}
