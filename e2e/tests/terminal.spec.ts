// Container logs and terminals through each example reverse proxy (#8,
// #27): the logs SSE stream is delivered unbuffered and the exec WebSocket
// (one-use ticket in the subprotocol, binary stdin/stdout framing, resize
// and exit messages) works through the TLS proxy with the session cookie.
//
// Needs a signed-in user and an environment with a connected agent and a
// running container that has /bin/sh (e.g. busybox), which the proxy-only
// E2E stack does not provision. Set:
//
//   E2E_TERMINAL_USER, E2E_TERMINAL_PASSWORD  a user holding
//     container.logs.read and container.exec on the container
//   E2E_TERMINAL_ENV        environment ID
//   E2E_TERMINAL_CONTAINER  container name
//
// Without them the spec is skipped. Contract: docs/api/streams.md.
import { expect, test, type Page } from '@playwright/test';
import { sseTimeline } from '../helpers/streams.js';

const user = process.env.E2E_TERMINAL_USER ?? '';
const password = process.env.E2E_TERMINAL_PASSWORD ?? '';
const envId = process.env.E2E_TERMINAL_ENV ?? '';
const container = process.env.E2E_TERMINAL_CONTAINER ?? '';
const configured = user !== '' && password !== '' && envId !== '' && container !== '';

const base = `/api/v1/environments/${encodeURIComponent(envId)}/containers/${encodeURIComponent(container)}`;

interface ExecSession {
	id: string;
	streamUrl: string;
	subprotocol: string;
	ticket: string;
	expiresAt: string;
}

interface TerminalRun {
	protocol: string;
	output: string;
	messages: string[];
	closeCode: number;
}

async function signIn(page: Page): Promise<void> {
	await page.goto('/');
	const status = await page.evaluate(
		async ({ username, password }) =>
			(
				await fetch('/api/v1/auth/session', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ username, password })
				})
			).status,
		{ username: user, password }
	);
	expect(status).toBe(200);
}

async function createSession(
	page: Page,
	body: object
): Promise<{ status: number; session: ExecSession }> {
	return page.evaluate(
		async ({ url, body }) => {
			const res = await fetch(url, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(body)
			});
			return { status: res.status, session: (await res.json()) as ExecSession };
		},
		{ url: `${base}/exec-sessions`, body }
	);
}

/**
 * Attaches to an exec session from the page (wss through the proxy),
 * resizes, sends input as stdin frames and collects stdout until the
 * server closes the socket.
 */
async function runTerminal(
	page: Page,
	s: ExecSession,
	input: string,
	protocols?: string[]
): Promise<TerminalRun> {
	return page.evaluate(
		({ path, input, protocols }) =>
			new Promise<TerminalRun>((resolve, reject) => {
				const url = new URL(path, location.href);
				url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
				const ws = new WebSocket(url, protocols);
				ws.binaryType = 'arraybuffer';
				const dec = new TextDecoder();
				let output = '';
				const messages: string[] = [];
				const timer = setTimeout(() => {
					ws.close();
					reject(new Error(`terminal timeout; output so far: ${output}`));
				}, 20_000);
				ws.onopen = () => {
					ws.send(JSON.stringify({ type: 'resize', cols: 120, rows: 40 }));
					const bytes = new TextEncoder().encode(input);
					const frame = new Uint8Array(bytes.length + 1);
					frame[0] = 0;
					frame.set(bytes, 1);
					ws.send(frame);
				};
				ws.onmessage = (e) => {
					if (typeof e.data === 'string') {
						messages.push(e.data);
						return;
					}
					const b = new Uint8Array(e.data as ArrayBuffer);
					output += dec.decode(b.subarray(1), { stream: true });
				};
				ws.onclose = (e) => {
					clearTimeout(timer);
					resolve({ protocol: ws.protocol, output, messages, closeCode: e.code });
				};
			}),
		{
			path: s.streamUrl,
			input,
			protocols: protocols ?? [s.subprotocol, `dockyard.ticket.${s.ticket}`]
		}
	);
}

test.describe('container logs and terminal', () => {
	test.skip(
		!configured,
		'set E2E_TERMINAL_USER, E2E_TERMINAL_PASSWORD, E2E_TERMINAL_ENV and E2E_TERMINAL_CONTAINER'
	);

	test('logs: bounded tail and an unbuffered SSE stream', async ({ page }) => {
		await signIn(page);
		const tail = await page.evaluate(async (url) => {
			const res = await fetch(url);
			return {
				status: res.status,
				cacheControl: res.headers.get('cache-control'),
				body: await res.json()
			};
		}, `${base}/logs?tail=5`);
		expect(tail.status).toBe(200);
		expect(tail.cacheControl).toContain('no-store');
		expect(tail.body.lines.length).toBeLessThanOrEqual(5);

		// Write a line to the container's main output through a terminal,
		// then read it back from the stream through the proxy.
		const marker = `e2e-log-${Date.now()}`;
		const { session } = await createSession(page, {
			command: ['/bin/sh', '-c', `echo ${marker} > /proc/1/fd/1`],
			tty: false
		});
		await runTerminal(page, session, '');
		const r = await sseTimeline(page, `${base}/logs/stream?tail=20`, {
			until: 'log',
			timeoutMs: 20_000
		});
		expect(r.status).toBe(200);
		expect(r.contentType).toContain('text/event-stream');
		expect(r.cacheControl).toContain('no-store');
		expect(r.lines.some((l) => l.line === 'event: log')).toBe(true);
	});

	test('terminal: ticket subprotocol, stdin/stdout, resize and exit through the proxy', async ({
		page
	}) => {
		await signIn(page);
		const { status, session } = await createSession(page, {
			command: ['/bin/sh'],
			tty: true,
			cols: 80,
			rows: 24
		});
		expect(status).toBe(201);
		expect(session.subprotocol).toBe('dockyard.exec.v1');
		expect(session.streamUrl).toContain(`/exec-sessions/${session.id}/stream`);

		const run = await runTerminal(page, session, 'stty size; echo dockyard-$((6*7)); exit 3\n');
		expect(run.protocol).toBe('dockyard.exec.v1');
		expect(run.output).toContain('40 120');
		expect(run.output).toContain('dockyard-42');
		expect(run.messages).toContain(JSON.stringify({ type: 'exit', code: 3 }));
		expect(run.closeCode).toBe(1000);

		// The ticket was one-use: attaching again is refused before the
		// upgrade (HTTP 404; browsers report the failed handshake as 1006).
		const again = await runTerminal(page, session, '');
		expect(again.closeCode).toBe(1006);
	});

	test('terminal: a wrong ticket is refused, a missing command is reported', async ({ page }) => {
		await signIn(page);
		const { session } = await createSession(page, { command: ['/bin/sh'] });
		const wrong = await runTerminal(page, session, '', [
			session.subprotocol,
			'dockyard.ticket.not-the-ticket'
		]);
		expect(wrong.closeCode).toBe(1006);

		const missing = await createSession(page, { command: ['/definitely/not/here'] });
		expect(missing.status).toBe(201);
		const run = await runTerminal(page, missing.session, '');
		expect(run.closeCode).toBe(4422);
		expect(run.messages.some((m) => m.includes('command_not_found'))).toBe(true);
	});
});
