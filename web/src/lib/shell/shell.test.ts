import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import type { Environment } from '$lib/api/client';
import { LiveStatus } from '$lib/live/status.svelte';
import PaletteHarness from '../../test/PaletteHarness.svelte';
import EnvironmentSwitcher from './EnvironmentSwitcher.svelte';
import LiveIndicator from './LiveIndicator.svelte';
import NoticesBell from './NoticesBell.svelte';
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
	it('announces the unread count and marks notices read when closed', async () => {
		const user = setup();
		const notices = new Notices(() => Date.now());
		notices.push({
			key: 'environment-offline:e2',
			kind: 'environment',
			tone: 'warn',
			title: 'edge is offline',
			href: '/environments/e2'
		});
		render(NoticesBell, { props: { notices } });
		const bell = screen.getByRole('button', { name: 'Notices, 1 unread' });
		await user.click(bell);
		expect(await screen.findByRole('link', { name: 'edge is offline' })).toHaveAttribute(
			'href',
			'/environments/e2'
		);
		await user.keyboard('{Escape}');
		await waitFor(() => expect(notices.unread).toBe(0));
		expect(screen.getByRole('button', { name: 'Notices' })).toBeInTheDocument();
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
		await user.keyboard('{ArrowDown}');
		expect(input).toHaveAttribute('aria-activedescendant', options[1].id);
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
