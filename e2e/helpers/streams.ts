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
