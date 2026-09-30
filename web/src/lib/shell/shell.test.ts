import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import type { Account, Environment } from '$lib/api/client';
import { LiveStatus } from '$lib/live/status.svelte';
import PaletteHarness from '../../test/PaletteHarness.svelte';
import QueryHarness from '../../test/QueryHarness.svelte';
import EnvironmentSwitcher from './EnvironmentSwitcher.svelte';
import LiveIndicator from './LiveIndicator.svelte';
import NoticesBell from './NoticesBell.svelte';
import UserMenu from './UserMenu.svelte';
import { accessOf, visibleNav } from './nav';
import { Notices } from './notices.svelte';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });
const env = (id: string, name: string, online: boolean) =>
	({ id, name, online, status: 'active', view: 'full', actions: [] }) as unknown as Environment;

afterEach(() => vi.unstubAllGlobals());

describe('EnvironmentSwitcher', () => {
	it('shows the choice with status and Engine version, and selects from the list', async () => {
		const user = setup();
		const onselect = vi.fn();
		const envs = [env('e1', 'homelab', true), env('e2', 'edge', false)];
		const { rerender } = render(EnvironmentSwitcher, {
			props: { environments: envs, selected: null, onselect, canAdd: true }
		});
		const trigger = screen.getByRole('button', {
			name: 'Environment: All environments, 1 of 2 online'
		});
		await user.click(trigger);
		const list = await screen.findByRole('listbox', { name: 'Environments' });
		const options = within(list).getAllByRole('option');
		expect(options.map((o) => o.textContent?.replace(/\s+/g, ' ').trim())).toEqual([
			'All environments 1 of 2 online',
			'homelab Online',
			'edge Offline'
		]);
		expect(options[0]).toHaveAttribute('aria-selected', 'true');
		expect(screen.getByRole('link', { name: 'Add environment' })).toHaveAttribute(
			'href',
			'/environments/add'
		);
		// Arrow keys move between options.
		await waitFor(() => expect(options[0]).toHaveFocus());
		await user.keyboard('{ArrowDown}');
		expect(options[1]).toHaveFocus();
		await user.keyboard('{Enter}');
		expect(onselect).toHaveBeenCalledWith('e1');

		await rerender({ selected: 'e1', engineVersion: '28.5.2' });
		expect(
			screen.getByRole('button', { name: 'Environment: homelab, Docker 28.5.2' })
		).toBeInTheDocument();
		await rerender({ selected: 'e2' });
		expect(
			screen.getByRole('button', { name: 'Environment: edge, Offline' })
		).toBeInTheDocument();
	});
});

describe('NoticesBell', () => {
	class Mem {
		m = new Map<string, string>();
		getItem(k: string) {
			return this.m.get(k) ?? null;
		}
		setItem(k: string, v: string) {
			this.m.set(k, v);
		}
	}
	const alert = (id: string, title: string, severity: 'critical' | 'warning', mine = true) => ({
		id,
		title,
		severity,
		link: `/environments/e1?tab=system`,
		startedAt: new Date(Date.now() - 3_600_000).toISOString(),
		actions: mine ? ['alert.dismiss'] : []
	});
	let posts: { path: string; body: unknown }[] = [];

	function mountBell(notices: Notices) {
		posts = [];
		vi.stubGlobal(
			'fetch',
			vi.fn(async (req: Request) => {
				const path = new URL(req.url).pathname;
				const text = req.method === 'POST' ? await req.text() : '';
				posts.push({ path, body: text ? JSON.parse(text) : undefined });
				const body =
					path === '/api/v1/alerts/dismissals'
						? { dismissed: 1, alertIds: ['a1'] }
						: { id: 'a1', dismissed: true };
				return new Response(JSON.stringify(body), {
					headers: { 'Content-Type': 'application/json' }
				});
			})
		);
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		type P = ComponentProps<typeof NoticesBell>;
		return render(QueryHarness<P>, {
			props: { client, component: NoticesBell, props: { notices, alertsHref: '/alerts' } }
		});
	}

	function seeded(storage = new Mem()) {
		const notices = new Notices(() => Date.now(), storage);
		notices.setAlerts([
			alert('a1', 'Disk /dev/sda on homelab is failing', 'critical'),
			alert('a2', 'edge is offline', 'warning', false)
		]);
		notices.push({
			key: 'job:j1',
			kind: 'job',
			tone: 'ok',
			title: 'Deployed Silo',
			href: '/jobs/j1'
		});
		return notices;
	}

	it('keeps the count after the list closes, until the items are dismissed', async () => {
		const user = setup();
		const notices = seeded();
		mountBell(notices);
		await user.click(screen.getByRole('button', { name: 'Notices, 3 items' }));
		const disk = await screen.findByRole('link', {
			name: 'Disk /dev/sda on homelab is failing'
		});
		expect(disk).toHaveAttribute('href', '/environments/e1?tab=system');
		expect(screen.getByText('Critical')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'View all alerts' })).toHaveAttribute(
			'href',
			'/alerts'
		);
		await user.keyboard('{Escape}');
		await waitFor(() =>
			expect(screen.queryByRole('link', { name: 'Deployed Silo' })).not.toBeInTheDocument()
		);
		// Closing reads nothing away: the badge stays.
		expect(screen.getByRole('button', { name: 'Notices, 3 items' })).toBeInTheDocument();
		expect(notices.count).toBe(3);
	});

	it('dismisses one item: an alert for everyone when allowed, else for this browser', async () => {
		const user = setup();
		const storage = new Mem();
		const notices = seeded(storage);
		mountBell(notices);
		await user.click(screen.getByRole('button', { name: 'Notices, 3 items' }));
		await user.click(
			await screen.findByRole('button', {
				name: 'Dismiss Disk /dev/sda on homelab is failing'
			})
		);
		await waitFor(() =>
			expect(posts.map((p) => p.path)).toEqual(['/api/v1/alerts/a1/dismissals'])
		);
		expect(notices.count).toBe(2);
		// Focus stays in the list.
		expect(screen.getByRole('button', { name: 'Dismiss edge is offline' })).toHaveFocus();

		// Not the user's to dismiss for everyone: hidden here, no request.
		await user.click(screen.getByRole('button', { name: 'Dismiss edge is offline' }));
		expect(posts).toHaveLength(1);
		await user.click(screen.getByRole('button', { name: 'Dismiss Deployed Silo' }));
		expect(notices.count).toBe(0);
		expect(screen.getByText('Nothing needs your attention.')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Notices' })).toBeInTheDocument();
		// Kept for the next visit (a new store reads them back).
		const next = new Notices(() => Date.now(), storage);
		next.setAlerts([alert('a2', 'edge is offline', 'warning', false)]);
		next.push({ key: 'job:j1', kind: 'job', tone: 'ok', title: 'Deployed Silo' });
		expect(next.count).toBe(0);
	});

	it('dismisses everything at once with one request for the alerts it may dismiss', async () => {
		const user = setup();
		const notices = seeded();
		mountBell(notices);
		await user.click(screen.getByRole('button', { name: 'Notices, 3 items' }));
		await user.click(await screen.findByRole('button', { name: 'Dismiss all' }));
		await waitFor(() =>
			expect(posts).toEqual([
				{ path: '/api/v1/alerts/dismissals', body: { alertIds: ['a1'] } }
			])
		);
		expect(notices.count).toBe(0);
		expect(notices.isDismissed('alert:a2:warning')).toBe(true);
		expect(notices.isDismissed('job:j1')).toBe(true);
		expect(screen.getByText('Nothing needs your attention.')).toBeInTheDocument();
	});
});

describe('LiveIndicator', () => {
	it('is silent while live and says so while reconnecting or polling', async () => {
		const status = new LiveStatus();
		render(LiveIndicator, { props: { status } });
		const region = screen.getByRole('status', { name: 'Live updates' });
		expect(region).toHaveTextContent('');
		status.set('live', 1);
		status.set('reconnecting', 2);
		await waitFor(() => expect(region).toHaveTextContent('Reconnecting…'));
		status.set('polling', 3);
		await waitFor(() => expect(region).toHaveTextContent('Live updates paused'));
		status.set('unauthenticated', 4);
		await waitFor(() => expect(region).toHaveTextContent(''));
	});
});

describe('CommandPalette', () => {
	it('finds pages and permission-filtered search hits and opens them from the keyboard', async () => {
		const user = setup();
		const fetchMock = vi.fn(async (req: Request) => {
			const url = new URL(req.url);
			expect(url.pathname).toBe('/api/v1/search');
			return new Response(
				JSON.stringify({
					query: url.searchParams.get('q'),
					items: [
						{
							type: 'stack',
							id: 's1',
							name: 'Silo',
							environmentId: 'e1',
							environmentName: 'homelab',
							stackId: 's1',
							status: 'deployed'
						},
						{
							type: 'container',
							id: 'c1',
							name: 'silo-silo-web-1',
							environmentId: 'e1',
							environmentName: 'homelab',
							status: 'running'
						}
					],
					gaps: [{ environmentId: 'e2', environmentName: 'edge', reason: 'offline' }]
				}),
				{ status: 200, headers: { 'Content-Type': 'application/json' } }
			);
		});
		vi.stubGlobal('fetch', fetchMock);
		const onnavigate = vi.fn();
		const pages = visibleNav(
			accessOf({ owner: true, entries: [], environments: [], catalogVersion: 1 } as never)
		);
		render(PaletteHarness, { props: { pages, onnavigate } });
		await user.click(screen.getByRole('button', { name: 'Search' }));
		const input = await screen.findByRole('combobox', {
			name: 'Search pages, environments, stacks and containers'
		});
		await waitFor(() => expect(input).toHaveFocus());
		// Pages first, without a request.
		expect(
			within(screen.getByRole('listbox', { name: 'Results' })).getAllByRole('option')
		).toHaveLength(pages.length);
		await user.type(input, 'si');
		await waitFor(() =>
			expect(screen.getByRole('option', { name: /Silo/ })).toBeInTheDocument()
		);
		expect(screen.getByText('Not searched: edge (offline).')).toBeInTheDocument();
		const options = screen.getAllByRole('option');
		expect(options[0]).toHaveAttribute('aria-selected', 'true');
		expect(input).toHaveAttribute('aria-activedescendant', options[0].id);
		// Walk down, one shown row at a time, to the container hit.
		const target = options.findIndex((o) => /silo-silo-web-1/.test(o.textContent ?? ''));
		expect(target).toBeGreaterThan(0);
		for (let k = 1; k <= target; k++) {
			await user.keyboard('{ArrowDown}');
			expect(input).toHaveAttribute('aria-activedescendant', options[k].id);
		}
		await user.keyboard('{Enter}');
		expect(onnavigate).toHaveBeenCalledWith('/containers/e1/silo-silo-web-1');
		expect(fetchMock).toHaveBeenCalled();
	});

	it('moves the highlight one shown row at a time, whatever order the hits came in', async () => {
		const user = setup();
		// Hits arrive in name order across types; the list shows them grouped.
		const hit = (type: string, id: string, name: string) => ({
			type,
			id,
			name,
			environmentId: 'e1',
			environmentName: 'homelab',
			stackId: type === 'stack' ? id : undefined
		});
		vi.stubGlobal(
			'fetch',
			vi.fn(
				async (req: Request) =>
					new Response(
						JSON.stringify({
							query: new URL(req.url).searchParams.get('q'),
							items: [
								hit('stack', 's1', 'Alpha'),
								hit('container', 'c1', 'Beta'),
								hit('stack', 's2', 'Gamma')
							],
							gaps: []
						}),
						{ status: 200, headers: { 'Content-Type': 'application/json' } }
					)
			)
		);
		render(PaletteHarness, { props: { pages: [], onnavigate: vi.fn() } });
		await user.click(screen.getByRole('button', { name: 'Search' }));
		const input = await screen.findByRole('combobox', {
			name: 'Search pages, environments, stacks and containers'
		});
		await user.type(input, 'a');
		await waitFor(() =>
			expect(screen.getByRole('option', { name: /Gamma/ })).toBeInTheDocument()
		);
		const options = screen.getAllByRole('option');
		for (let i = 0; i < options.length; i++) {
			expect(input).toHaveAttribute('aria-activedescendant', options[i].id);
			expect(options[i]).toHaveAttribute('aria-selected', 'true');
			await user.keyboard('{ArrowDown}');
		}
		// Stays on the last row, and walks back up in the same order.
		expect(input).toHaveAttribute('aria-activedescendant', options[options.length - 1].id);
		await user.keyboard('{ArrowUp}');
		expect(input).toHaveAttribute('aria-activedescendant', options[options.length - 2].id);
	});

	it('offers recent pages and permitted actions before any query', async () => {
		const user = setup();
		const access = accessOf({
			owner: false,
			catalogVersion: 1,
			environments: [],
			entries: [
				{
					capability: 'stack.create',
					allowed: true,
					source: 'group_rule',
					reason: '',
					scope: { kind: 'instance' }
				}
			]
		} as never);
		const pages = visibleNav(access);
		render(PaletteHarness, {
			props: {
				pages,
				onnavigate: vi.fn(),
				access,
				recent: [
					{ path: '/', title: 'Dashboard' },
					{ path: '/stacks/s1', title: 'Silo' },
					{ path: '/stacks' },
					{ path: '/stacks/s2' }
				]
			}
		});
		await user.click(screen.getByRole('button', { name: 'Search' }));
		const list = await screen.findByRole('listbox', { name: 'Results' });
		// The current page and unnamed detail pages are left out of Recent.
		const recent = within(list).getByRole('group', { name: 'Recent' });
		expect(
			within(recent)
				.getAllByRole('option')
				.map((o) => o.textContent?.replace(/\s+/g, ' ').trim())
		).toEqual(['Silo Stacks', 'Stacks']);
		// Only the actions the caller may start.
		const actions = within(list).getByRole('group', { name: 'Actions' });
		expect(
			within(actions)
				.getAllByRole('option')
				.map((o) => o.textContent?.trim())
		).toEqual(['Create stack']);
		expect(within(list).getByRole('group', { name: 'Pages' })).toBeInTheDocument();
		// Phones get a visible way out.
		await user.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull());
	});
});

describe('UserMenu', () => {
	it('opens the personal Profile and its API tokens, and signs out', async () => {
		const user = setup();
		const onsignout = vi.fn();
		const account = { id: 'u1', username: 'ada', displayName: 'Ada', owner: true } as Account;
		render(UserMenu, { props: { user: account, onsignout } });
		await user.click(screen.getByRole('button', { name: 'Account menu for Ada' }));
		const items = await screen.findAllByRole('menuitem');
		expect(items.map((i) => i.textContent?.trim())).toEqual([
			'Profile',
			'API tokens',
			'Sign out'
		]);
		expect(screen.getByRole('menuitem', { name: 'Profile' })).toHaveAttribute(
			'href',
			'/profile'
		);
		expect(screen.getByRole('menuitem', { name: 'API tokens' })).toHaveAttribute(
			'href',
			'/profile/tokens'
		);
		// The old combined entry is gone; Settings stays in the sidebar.
		expect(screen.queryByRole('menuitem', { name: /security|settings/i })).toBeNull();
		await user.click(screen.getByRole('menuitem', { name: 'Sign out' }));
		expect(onsignout).toHaveBeenCalledOnce();
	});
});
