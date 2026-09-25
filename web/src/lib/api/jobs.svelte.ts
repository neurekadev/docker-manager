// Job progress subscription (#26, docs/api/streams.md "Job events"). A
// JobWatcher follows GET /api/v1/jobs/{id}/events/stream (SSE): the first
// `job` event is the full job, then state/progress/item/log/warning events.
// EventSource resumes with Last-Event-ID after a network blip; when the
// stream cannot be opened (or ends without a terminal state) the watcher
// polls GET /jobs/{id} until the job finishes. Nothing here mutates.
import type { Job, JobEvent, JobItem } from './client';
import { api, unwrap, type ApiClient } from './client';

export const TERMINAL_STATES = [
	'succeeded',
	'failed',
	'partial',
	'cancelled',
	'interrupted'
] as const;

export function isTerminal(state: string | undefined): boolean {
	return !!state && (TERMINAL_STATES as readonly string[]).includes(state);
}

/** The parts of EventSource the watcher uses (fakeable in tests). */
export interface EventSourceLike {
	readonly readyState: number;
	addEventListener(type: string, listener: (ev: MessageEvent) => void): void;
	close(): void;
	onerror: ((ev: Event) => void) | null;
}

export interface JobWatcherOptions {
	client?: ApiClient;
	eventSource?: (url: string) => EventSourceLike;
	/** Poll interval of the fallback, ms. */
	pollMs?: number;
	/** Called once with the job when it reaches a terminal state. */
	onfinish?: (job: Job) => void;
}

const MAX_LOG = 50;

export interface LogLine {
	seq: number;
	at: string;
	message: string;
	warning: boolean;
}

export class JobWatcher {
	readonly id: string;
	job = $state<Job | null>(null);
	items = $state<JobItem[]>([]);
	log = $state<LogLine[]>([]);
	/** The stream is open (false while polling or after the end). */
	streaming = $state(false);
	error = $state<unknown>(null);

	readonly state = $derived(this.job?.state ?? 'queued');
	readonly terminal = $derived(isTerminal(this.job?.state));
	readonly failedItems = $derived(this.items.filter((i) => i.status === 'failed'));

	#opts: JobWatcherOptions;
	#es: EventSourceLike | null = null;
	#poll: ReturnType<typeof setTimeout> | null = null;
	#finished = false;
	#stopped = false;
	#lastSeq = 0;

	constructor(id: string, opts: JobWatcherOptions = {}) {
		this.id = id;
		this.#opts = opts;
	}

	/** Starts following the job; returns stop(). */
	start(): () => void {
		this.#open();
		return () => this.stop();
	}

	stop() {
		this.#stopped = true;
		this.#es?.close();
		this.#es = null;
		if (this.#poll) clearTimeout(this.#poll);
		this.#poll = null;
		this.streaming = false;
	}

	#open() {
		const factory =
			this.#opts.eventSource ??
			((url: string) =>
				new EventSource(url, { withCredentials: true }) as unknown as EventSourceLike);
		let es: EventSourceLike;
		try {
			es = factory(`/api/v1/jobs/${encodeURIComponent(this.id)}/events/stream`);
		} catch (e) {
			this.error = e;
			this.#startPolling();
			return;
		}
		this.#es = es;
		this.streaming = true;
		es.addEventListener('job', (ev) => this.#setJob(JSON.parse(ev.data) as Job));
		for (const type of ['state', 'progress', 'item', 'log', 'warning']) {
			es.addEventListener(type, (ev) => this.apply(JSON.parse(ev.data) as JobEvent));
		}
		es.addEventListener('close', () => {
			// Terminal events arrive before close; otherwise permissions or the
			// session changed: fall back to polling (which reports 401/404).
			this.#closeStream();
			if (!this.terminal) this.#startPolling();
		});
		es.onerror = () => {
			// CLOSED (2): the browser gave up (e.g. 404/403 before the stream).
			if (es.readyState === 2) {
				this.#closeStream();
				if (!this.terminal) this.#startPolling();
			}
		};
	}

	#closeStream() {
		this.#es?.close();
		this.#es = null;
		this.streaming = false;
	}

	#setJob(job: Job) {
		this.job = job;
		this.items = [...(job.items ?? [])];
		this.#checkFinished();
	}

	/** Applies one job event (exported for tests and the live client). */
	apply(ev: JobEvent) {
		if (ev.seq <= this.#lastSeq) return; // replayed after a reconnect
		this.#lastSeq = ev.seq;
		const job = this.job;
		switch (ev.type) {
			case 'state':
				if (job && ev.state) this.job = { ...job, state: ev.state as Job['state'] };
				break;
			case 'progress':
				if (job)
					this.job = {
						...job,
						progress: {
							percent: ev.percent ?? job.progress.percent,
							step: ev.step ?? job.progress.step,
							message: ev.message
						}
					};
				break;
			case 'item':
				if (ev.item) {
					const i = this.items.findIndex((x) => x.name === ev.item!.name);
					this.items = i >= 0 ? this.items.with(i, ev.item) : [...this.items, ev.item];
				}
				break;
			case 'log':
			case 'warning':
				if (ev.message)
					this.log = [
						...this.log,
						{
							seq: ev.seq,
							at: ev.at,
							message: ev.message,
							warning: ev.type === 'warning'
						}
					].slice(-MAX_LOG);
				break;
		}
		if (ev.type === 'state' && isTerminal(ev.state)) {
			// The terminal job (error, finishedAt) comes from GET.
			void this.#refresh();
		}
	}

	async #refresh() {
		try {
			const job = await unwrap(
				(this.#opts.client ?? api).GET('/api/v1/jobs/{jobId}', {
					params: { path: { jobId: this.id } }
				})
			);
			this.job = job;
			if (job.items?.length) this.items = [...job.items];
			this.error = null;
		} catch (e) {
			this.error = e;
		}
		this.#checkFinished();
	}

	#startPolling() {
		if (this.#stopped || this.#poll) return;
		const tick = async () => {
			this.#poll = null;
			await this.#refresh();
			if (!this.terminal && !this.#stopped)
				this.#poll = setTimeout(tick, this.#opts.pollMs ?? 3000);
		};
		void tick();
	}

	#checkFinished() {
		if (this.#finished || !this.job || !isTerminal(this.job.state)) return;
		this.#finished = true;
		this.#closeStream();
		this.#opts.onfinish?.(this.job);
	}
}
