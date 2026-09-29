// Signed-in devices (#16): the rows name the device in words, mark this
// device, and Sign out ends one device (my own through /me, a user's
// through /users/{id}); this device signs out like the user menu does.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import { toast } from '$lib/ui';
import QueryHarness from '../../../test/QueryHarness.svelte';
import SessionsTable from './SessionsTable.svelte';
import type { UserSession } from './queries';

const nav = vi.hoisted(() => ({
	page: { url: new URL('http://localhost/profile/sessions') },
	goto: vi.fn()
}));
vi.mock('$app/state', () => ({ page: nav.page }));
vi.mock('$app/navigation', () => ({ goto: nav.goto, beforeNavigate: vi.fn() }));

const FIREFOX_WINDOWS =
	'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0';
const CHROME_ANDROID =
	'Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36';

function session(id: string, p: Partial<UserSession> = {}): UserSession {
	const recent = new Date(Date.now() - 5 * 60_000).toISOString();
	return {
		id,
		current: false,
		staySignedIn: false,
		createdAt: recent,
		lastSeenAt: recent,
		expiresAt: new Date(Date.now() + 86_400_000).toISOString(),
		idleExpiresAt: new Date(Date.now() + 8 * 3_600_000).toISOString(),
		...p
	};
}

let items: UserSession[] = [];
let calls: { method: string; path: string }[] = [];

beforeEach(() => {
	items = [
		session('s-1', {
			current: true,
			staySignedIn: true,
			ip: '203.0.113.7',
			userAgent: FIREFOX_WINDOWS
		}),
		session('s-2', { ip: '198.51.100.4', userAgent: CHROME_ANDROID })
	];
	calls = [];
	nav.goto.mockReset();
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		calls.push({ method: req.method, path: url.pathname });
		const json = (status: number, body: unknown) =>
			new Response(JSON.stringify(body), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (req.method === 'DELETE') return new Response(null, { status: 204 });
		if (url.pathname === '/api/v1/me/sessions' || url.pathname === '/api/v1/users/u-1/sessions')
			return json(200, { items });
		return json(404, {
			code: 'not_found',
			message: 'no',
			details: [],
			requestId: 'r',
			retryable: false
		});
	});
});
afterEach(() => {
	vi.unstubAllGlobals();
	toast.clear();
});

function show(props: { userId?: string; label?: string } = {}) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: SessionsTable as unknown as Component<Record<string, unknown>>,
			props
		}
	});
}

describe('SessionsTable', () => {
	it('lists my devices in words and marks this device', async () => {
		show();
		const table = await screen.findByRole('table', { name: 'Signed-in devices' });
		const rows = within(table).getAllByRole('row').slice(1);
		expect(rows).toHaveLength(2);
		const mine = rows.find((r) => within(r).queryByText('This device'));
		expect(mine).toBeDefined();
		expect(within(mine!).getByText('Firefox on Windows')).toBeInTheDocument();
		expect(within(mine!).getByText('203.0.113.7')).toBeInTheDocument();
		expect(within(mine!).getByText('Yes')).toBeInTheDocument();
		const other = rows.find((r) => r !== mine)!;
		expect(within(other).getByText('Chrome on Android')).toBeInTheDocument();
		expect(within(other).queryByText('This device')).toBeNull();
		expect(within(other).getByText('No')).toBeInTheDocument();
		expect(calls).toContainEqual({ method: 'GET', path: '/api/v1/me/sessions' });
	});

	it('signs out another device of mine without asking', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		show();
		await user.click(await screen.findByRole('button', { name: 'Sign out Chrome on Android' }));
		await waitFor(() =>
			expect(calls).toContainEqual({ method: 'DELETE', path: '/api/v1/me/sessions/s-2' })
		);
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toContain('Signed out Chrome on Android')
		);
		expect(nav.goto).not.toHaveBeenCalled();
	});

	it('signs this device out like the user menu', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		show();
		await user.click(
			await screen.findByRole('button', {
				name: 'Sign out Firefox on Windows (this device)'
			})
		);
		await waitFor(() =>
			expect(calls).toContainEqual({ method: 'DELETE', path: '/api/v1/auth/session' })
		);
		expect(calls).not.toContainEqual({ method: 'DELETE', path: '/api/v1/me/sessions/s-1' });
		await waitFor(() => expect(nav.goto).toHaveBeenCalled());
		expect(String(nav.goto.mock.calls[0][0])).toMatch(/^\/sign-in/);
	});

	it("uses the user's endpoints for the owner", async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		items = [session('s-9', { userAgent: CHROME_ANDROID })];
		show({ userId: 'u-1', label: 'Signed-in devices of Ada' });
		await screen.findByRole('table', { name: 'Signed-in devices of Ada' });
		expect(calls).toContainEqual({ method: 'GET', path: '/api/v1/users/u-1/sessions' });
		await user.click(screen.getByRole('button', { name: 'Sign out Chrome on Android' }));
		await waitFor(() =>
			expect(calls).toContainEqual({
				method: 'DELETE',
				path: '/api/v1/users/u-1/sessions/s-9'
			})
		);
	});

	it('says so when no device is signed in', async () => {
		items = [];
		show({ userId: 'u-1' });
		expect(await screen.findByText('No signed-in devices.')).toBeInTheDocument();
		expect(screen.queryByRole('table')).toBeNull();
	});

	it('names an unknown browser plainly and shows a missing address as unknown', async () => {
		items = [session('s-3')];
		show();
		const table = await screen.findByRole('table', { name: 'Signed-in devices' });
		expect(within(table).getByText('Unknown device')).toBeInTheDocument();
		expect(within(table).getByText('Unknown')).toBeInTheDocument();
	});
});
