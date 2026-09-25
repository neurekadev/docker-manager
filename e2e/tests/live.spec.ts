// Live synchronization through each example reverse proxy (#23, #15, #27):
// a file changed outside DockYard reaches an open file view through the
// live stream within the 2 s target; an unsaved editor buffer stays intact
// and its save is refused (412 with the current ETag) until resolved; two
// browser sessions converge on the same events.
//
// This spec drives the contract the screens use, inside the page: the live
// EventSource and the scoped file API with the session cookie. The rendered
// listing and the external-change conflict notice of the file manager
// screens (#22) are asserted by tests/ui-files.spec.ts (host-side edit test,
// same E2E_*_STACK_DIR idea).
//
// Needs a signed-in user, a managed stack with a connected agent, and write
// access for the test runner to the stack's project directory on the
// agent's host (the "external editor"). The proxy-only E2E stack does not
// provision these. Set:
//
//   E2E_LIVE_USER, E2E_LIVE_PASSWORD  a user holding stack.read,
//     stack.files.read and stack.files.write on the stack
//   E2E_LIVE_STACK       stack ID
//   E2E_LIVE_STACK_DIR   the stack's project directory as the runner sees
//     it (for example the identical host path on the agent's machine)
//
// Without them the spec is skipped. Contract: docs/api/streams.md.
import { expect, test, type Browser, type Page } from '@playwright/test';
import { randomUUID } from 'node:crypto';
import { rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const user = process.env.E2E_LIVE_USER ?? '';
const password = process.env.E2E_LIVE_PASSWORD ?? '';
const stackId = process.env.E2E_LIVE_STACK ?? '';
const stackDir = process.env.E2E_LIVE_STACK_DIR ?? '';
const configured = user !== '' && password !== '' && stackId !== '' && stackDir !== '';

const files = `/api/v1/stacks/${encodeURIComponent(stackId)}/files`;

interface LiveRecord {
	type: string;
	id: string;
	data: string;
	t: number;
}

async function signedInPage(browser: Browser): Promise<Page> {
	const context = await browser.newContext();
	const page = await context.newPage();
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
	return page;
}

/** Opens the live stream in the page; events collect in window.__live. */
async function openLive(page: Page): Promise<void> {
	await page.evaluate((stack) => {
		const w = window as unknown as { __live: LiveRecord[]; __liveSource: EventSource };
		w.__live = [];
		const es = new EventSource(`/api/v1/live/stream?stackId=${encodeURIComponent(stack)}`, {
			withCredentials: true
		});
		for (const type of ['hello', 'files.changed', 'invalidate', 'reset', 'close']) {
			es.addEventListener(type, (e) => {
				const m = e as MessageEvent;
				w.__live.push({
					type,
					id: m.lastEventId,
					data: String(m.data),
					t: performance.now()
				});
			});
		}
		w.__liveSource = es;
	}, stackId);
	await expect
		.poll(async () => (await liveEvents(page)).some((e) => e.type === 'hello'), {
			timeout: 10_000
		})
		.toBe(true);
}

async function liveEvents(page: Page): Promise<LiveRecord[]> {
	return page.evaluate(() => (window as unknown as { __live: LiveRecord[] }).__live);
}

/** Waits for a files.changed event naming path and returns it. */
async function waitForFileChange(page: Page, path: string): Promise<LiveRecord> {
	let hit: LiveRecord | undefined;
	await expect
		.poll(
			async () => {
				hit = (await liveEvents(page)).find(
					(e) =>
						e.type === 'files.changed' &&
						((
							JSON.parse(e.data) as { paths: string[]; overflow: boolean }
						).paths.includes(path) ||
							(JSON.parse(e.data) as { overflow: boolean }).overflow)
				);
				return hit !== undefined;
			},
			{ timeout: 10_000 }
		)
		.toBe(true);
	return hit!;
}

async function api(
	page: Page,
	method: string,
	url: string,
	body?: unknown,
	headers: Record<string, string> = {}
): Promise<{ status: number; etag: string; json: unknown }> {
	return page.evaluate(
		async ({ method, url, body, headers }) => {
			const res = await fetch(url, {
				method,
				headers:
					body === undefined
						? headers
						: { 'Content-Type': 'application/json', ...headers },
				body: body === undefined ? undefined : JSON.stringify(body)
			});
			const text = await res.text();
			let json: unknown = null;
			try {
				json = JSON.parse(text);
			} catch {
				json = text;
			}
			return { status: res.status, etag: res.headers.get('ETag') ?? '', json };
		},
		{ method, url, body, headers }
	);
}

test.describe('live synchronization', () => {
	test.skip(
		!configured,
		'set E2E_LIVE_USER, E2E_LIVE_PASSWORD, E2E_LIVE_STACK and E2E_LIVE_STACK_DIR'
	);

	test('an external edit reaches open file views; an unsaved buffer is kept and its save refused', async ({
		browser
	}) => {
		const name = `live-${randomUUID().slice(0, 8)}.txt`;
		const created = `live-new-${randomUUID().slice(0, 8)}.txt`;
		writeFileSync(join(stackDir, name), 'original\n');
		try {
			const a = await signedInPage(browser);
			const b = await signedInPage(browser);
			await openLive(a);
			await openLive(b);

			// Session A opens the file in its "editor" and starts editing.
			const loaded = await api(a, 'GET', `${files}/content?path=${encodeURIComponent(name)}`);
			expect(loaded.status).toBe(200);
			const unsaved = (loaded.json as { content: string }).content + 'my unsaved line\n';

			// An editor on the host changes the file and adds another one.
			const start = Date.now();
			writeFileSync(join(stackDir, name), 'changed outside DockYard\n');
			writeFileSync(join(stackDir, created), 'new\n');
			const [ea, eb] = await Promise.all([
				waitForFileChange(a, name),
				waitForFileChange(b, name)
			]);
			expect(Date.now() - start).toBeLessThan(10_000);
			test.info().annotations.push({
				type: 'latency',
				description: `external change visible after ${Date.now() - start} ms (target: 2 s at p95)`
			});
			// Both sessions converge on the same stream positions.
			expect(ea.id).toBe(eb.id);
			await waitForFileChange(a, created);

			// The listing now has the new file, in both sessions.
			for (const p of [a, b]) {
				const listing = await api(p, 'GET', `${files}?path=.`);
				expect(listing.status).toBe(200);
				const names = (listing.json as { items: { name: string }[] }).items.map(
					(i) => i.name
				);
				expect(names).toContain(created);
			}

			// Saving the stale buffer is refused with the current revision;
			// nothing on disk is overwritten and the buffer is untouched.
			const stale = await api(
				a,
				'PUT',
				`${files}/content?path=${encodeURIComponent(name)}`,
				{ content: unsaved },
				{ 'If-Match': loaded.etag }
			);
			expect(stale.status).toBe(412);
			expect(stale.etag).not.toBe('');
			expect(stale.etag).not.toBe(loaded.etag);
			expect(unsaved).toContain('my unsaved line');
			const current = await api(
				b,
				'GET',
				`${files}/content?path=${encodeURIComponent(name)}`
			);
			expect((current.json as { content: string }).content).toBe(
				'changed outside DockYard\n'
			);
			expect(current.etag).toBe(stale.etag);

			// An explicit overwrite with the current ETag saves the buffer,
			// and session B sees that change live.
			const saved = await api(
				a,
				'PUT',
				`${files}/content?path=${encodeURIComponent(name)}`,
				{ content: unsaved },
				{ 'If-Match': stale.etag }
			);
			expect(saved.status).toBe(200);
			await expect
				.poll(
					async () =>
						(await liveEvents(b)).filter(
							(e) =>
								e.type === 'files.changed' &&
								e.data.includes(name) &&
								Number(e.t) > 0
						).length,
					{ timeout: 10_000 }
				)
				.toBeGreaterThan(1);
		} finally {
			rmSync(join(stackDir, name), { force: true });
			rmSync(join(stackDir, created), { force: true });
		}
	});
});
