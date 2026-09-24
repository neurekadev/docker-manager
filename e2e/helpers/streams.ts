// SSE and WebSocket helpers that run inside the page, so traffic takes the
// real browser path through the TLS proxy (same origin, cookies, CSP)
// (#23, #27, #29).
import type { Page } from '@playwright/test';

export interface SseEvent {
	type: string;
	data: string;
}

/**
 * Opens an EventSource at url (relative to the page origin) and resolves
 * with the events received until `until` returns true for one of them (or
 * `max` events), then closes it. Rejects on stream error or timeout.
 */
export async function collectSse(
	page: Page,
	url: string,
	opts: { eventTypes?: string[]; max?: number; timeoutMs?: number; untilType?: string } = {}
): Promise<SseEvent[]> {
	return page.evaluate(
		({ url, types, max, timeoutMs, untilType }) =>
			new Promise<{ type: string; data: string }[]>((resolve, reject) => {
				const events: { type: string; data: string }[] = [];
				const es = new EventSource(url, { withCredentials: true });
				const timer = setTimeout(() => {
					es.close();
					reject(new Error(`SSE timeout after ${events.length} events`));
				}, timeoutMs);
				const onEvent = (e: MessageEvent) => {
					events.push({ type: e.type, data: String(e.data) });
					if (events.length >= max || e.type === untilType) {
						clearTimeout(timer);
						es.close();
						resolve(events);
					}
				};
				for (const t of types) es.addEventListener(t, onEvent as EventListener);
				es.onerror = () => {
					if (es.readyState === EventSource.CLOSED) {
						clearTimeout(timer);
						reject(new Error(`SSE stream failed after ${events.length} events`));
					}
				};
			}),
		{
			url,
			types: opts.eventTypes ?? ['message'],
			max: opts.max ?? 1000,
			timeoutMs: opts.timeoutMs ?? 10_000,
			untilType: opts.untilType ?? ''
		}
	);
}

/**
 * Opens a WebSocket at path on the page origin (wss for https pages),
 * optionally with subprotocols, sends each message and resolves with the
 * replies (one per message). Rejects on close/error or timeout.
 */
export async function wsRoundTrip(
	page: Page,
	path: string,
	messages: string[],
	opts: { protocols?: string[]; timeoutMs?: number } = {}
): Promise<{ protocol: string; replies: string[] }> {
	return page.evaluate(
		({ path, messages, protocols, timeoutMs }) =>
			new Promise<{ protocol: string; replies: string[] }>((resolve, reject) => {
				const url = new URL(path, location.href);
				url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
				const ws = new WebSocket(url, protocols);
				const replies: string[] = [];
				const timer = setTimeout(() => {
					ws.close();
					reject(new Error(`WebSocket timeout after ${replies.length} replies`));
				}, timeoutMs);
				ws.onopen = () => messages.forEach((m) => ws.send(m));
				ws.onmessage = (e) => {
					replies.push(String(e.data));
					if (replies.length === messages.length) {
						clearTimeout(timer);
						const protocol = ws.protocol;
						ws.close(1000);
						resolve({ protocol, replies });
					}
				};
				ws.onerror = () => {
					clearTimeout(timer);
					reject(new Error('WebSocket error'));
				};
				ws.onclose = (e) => {
					if (replies.length < messages.length) {
						clearTimeout(timer);
						reject(new Error(`WebSocket closed early: ${e.code} ${e.reason}`));
					}
				};
			}),
		{ path, messages, protocols: opts.protocols ?? [], timeoutMs: opts.timeoutMs ?? 10_000 }
	);
}

export interface TimedLine {
	/** Milliseconds since the request started. */
	t: number;
	line: string;
}

/**
 * Reads an SSE response with fetch inside the page and records every
 * non-empty line (including ": heartbeat" comments, which EventSource
 * hides) with its arrival time, until the line `event: <until>` arrives.
 * Arrival times show whether a proxy streams or buffers (#27).
 */
export async function sseTimeline(
	page: Page,
	url: string,
	opts: { until: string; timeoutMs?: number }
): Promise<{ status: number; contentType: string; cacheControl: string; lines: TimedLine[] }> {
	return page.evaluate(
		async ({ url, until, timeoutMs }) => {
			const ctrl = new AbortController();
			const timer = setTimeout(() => ctrl.abort(new Error('timeout')), timeoutMs);
			const start = performance.now();
			const lines: { t: number; line: string }[] = [];
			const res = await fetch(url, { signal: ctrl.signal, headers: { Accept: 'text/event-stream' } });
			const out = {
				status: res.status,
				contentType: res.headers.get('content-type') ?? '',
				cacheControl: res.headers.get('cache-control') ?? '',
				lines
			};
			const reader = res.body!.pipeThrough(new TextDecoderStream()).getReader();
			let buf = '';
			try {
				for (;;) {
					const { value, done } = await reader.read();
					if (done) throw new Error(`stream ended before "event: ${until}"`);
					buf += value;
					for (let i = buf.indexOf('\n'); i >= 0; i = buf.indexOf('\n')) {
						const line = buf.slice(0, i).replace(/\r$/, '');
						buf = buf.slice(i + 1);
						if (line === '') continue;
						lines.push({ t: performance.now() - start, line });
						if (line === `event: ${until}`) return out;
					}
				}
			} catch (e) {
				throw new Error(`${(e as Error).message}; received ${JSON.stringify(lines)}`);
			} finally {
				clearTimeout(timer);
				ctrl.abort();
			}
		},
		{ url, until: opts.until, timeoutMs: opts.timeoutMs ?? 30_000 }
	);
}

/**
 * Opens a WebSocket at path (wss on https pages), records the first
 * message (a hello), sends "before-idle", waits idleMs without any
 * application traffic, sends "after-idle" and resolves with every message
 * received. Rejects if the connection closes early or on timeout. Only
 * WebSocket pings (answered by the browser) cross the connection while it
 * is idle.
 */
export async function wsIdleRoundTrip(
	page: Page,
	path: string,
	opts: { idleMs: number; protocols?: string[]; timeoutMs?: number }
): Promise<{ protocol: string; messages: string[] }> {
	return page.evaluate(
		({ path, idleMs, protocols, timeoutMs }) =>
			new Promise<{ protocol: string; messages: string[] }>((resolve, reject) => {
				const url = new URL(path, location.href);
				url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
				const ws = new WebSocket(url, protocols);
				const messages: string[] = [];
				let done = false;
				const timer = setTimeout(() => {
					done = true;
					ws.close();
					reject(new Error(`WebSocket timeout after ${JSON.stringify(messages)}`));
				}, timeoutMs);
				ws.onmessage = (e) => {
					const data = String(e.data);
					messages.push(data);
					if (messages.length === 1) {
						ws.send('before-idle');
					} else if (data === 'echo:before-idle') {
						setTimeout(() => ws.send('after-idle'), idleMs);
					} else if (data === 'echo:after-idle') {
						done = true;
						clearTimeout(timer);
						const protocol = ws.protocol;
						ws.close(1000);
						resolve({ protocol, messages });
					}
				};
				ws.onclose = (e) => {
					if (!done) {
						done = true;
						clearTimeout(timer);
						reject(new Error(`WebSocket closed early: ${e.code} ${e.reason} after ${JSON.stringify(messages)}`));
					}
				};
			}),
		{ path, idleMs: opts.idleMs, protocols: opts.protocols ?? [], timeoutMs: opts.timeoutMs ?? opts.idleMs + 30_000 }
	);
}
