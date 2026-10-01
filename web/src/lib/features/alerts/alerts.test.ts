// The Alerts page (#159, AlertsView) and the System tab's alert marks.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import { LIST_FILTERS_PREFIX } from '$lib/features/resources/list-filters.svelte';
import AlertMark from './AlertMark.svelte';
import AlertsView from './AlertsView.svelte';
import type { Alert } from './model';
import { degradedArray, failingDisk, offlineEdge, sampleAlert } from './test/samples';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });
const json = (body: unknown) =>
	new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });

let requests: { method: string; path: string; search: URLSearchParams; body: unknown }[] = [];

function stub(alerts: (search: URLSearchParams) => Alert[]) {
	requests = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (req: Request) => {
			const url = new URL(req.url);
			const text = req.method === 'POST' ? await req.text() : '';
			requests.push({
				method: req.method,
				path: url.pathname,
				search: url.searchParams,
				body: text ? JSON.parse(text) : undefined
			});
			if (url.pathname === '/api/v1/environments')
				return json({
					items: [
						{ id: 'e1', name: 'homelab', online: true, actions: [] },
						{ id: 'e2', name: 'edge', online: false, actions: [] }
					]
				});
			if (url.pathname === '/api/v1/alerts') return json({ items: alerts(url.searchParams) });
			if (url.pathname === '/api/v1/alerts/dismissals') {
				const ids = (JSON.parse(text) as { alertIds: string[] }).alertIds;
				return json({ dismissed: ids.length, alertIds: ids });
			}
			if (url.pathname.endsWith('/dismissals'))
				return json({ ...failingDisk, dismissed: true });
			return new Response('{}', { status: 404 });
		})
	);
}

function mount<P extends Record<string, unknown>>(component: Component<P>, props: P) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(QueryHarness<P>, { props: { client, component, props } });
}

const alertGets = () => requests.filter((r) => r.method === 'GET' && r.path === '/api/v1/alerts');

beforeEach(() => sessionStorage.clear());
afterEach(() => vi.unstubAllGlobals());

describe('Alerts page (#159)', () => {
	it('lists the active alerts with severity, environment and time, and dismisses one', async () => {
		const user = setup();
		stub(() => [failingDisk, degradedArray, offlineEdge]);
		mount(AlertsView, { environmentId: null });
		const table = await screen.findByRole('table', { name: 'Alerts' });
		expect(alertGets()[0].search.get('state')).toBe('active');
		const disk = within(table).getByRole('link', {
			name: 'Disk /dev/sda on homelab is failing'
		});
		expect(disk).toHaveAttribute('href', '/environments/e1?tab=system');
		const row = disk.closest('tr')!;
		expect(within(row).getByText('Critical')).toBeInTheDocument();
		expect(within(row).getByText('homelab')).toBeInTheDocument();
		expect(row).toHaveTextContent('SMART self-assessment failed, 8 reallocated sectors.');
		expect(screen.getAllByText('3 alerts').length).toBeGreaterThan(0);
		// Only alerts the user may dismiss have the button.
		expect(within(table).getAllByRole('button', { name: /^Dismiss / })).toHaveLength(2);
		const raidRow = within(table)
			.getByRole('link', { name: /RAID md0/ })
			.closest('tr')!;
		expect(within(raidRow).queryByRole('button')).not.toBeInTheDocument();

		await user.click(
			within(row).getByRole('button', { name: 'Dismiss Disk /dev/sda on homelab is failing' })
		);
		await waitFor(() =>
			expect(requests.some((r) => r.path === '/api/v1/alerts/a1/dismissals')).toBe(true)
		);
		expect(
			(await screen.findAllByText('Dismissed Disk /dev/sda on homelab is failing')).length
		).toBeGreaterThan(0);
	});

	it('dismisses every listed alert the user may dismiss after confirming', async () => {
		const user = setup();
		stub(() => [failingDisk, degradedArray, offlineEdge]);
		mount(AlertsView, { environmentId: null });
		await screen.findByRole('table', { name: 'Alerts' });
		await user.click(screen.getByRole('button', { name: 'Dismiss all' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Dismiss all alerts' });
		expect(dialog).toHaveTextContent(
			'Dismisses 2 alerts for everyone. They stay in Alerts and open again if they get worse.'
		);
		expect(dialog).toHaveTextContent('1 alert you may not dismiss stays active.');
		await user.click(within(dialog).getByRole('button', { name: 'Dismiss 2 alerts' }));
		await waitFor(() =>
			expect(requests.find((r) => r.path === '/api/v1/alerts/dismissals')?.body).toEqual({
				alertIds: ['a1', 'a3']
			})
		);
		expect((await screen.findAllByText('Dismissed 2 alerts')).length).toBeGreaterThan(0);
	});

	it('names the kind and the short values of a host alert under its detail', async () => {
		stub(() => [
			sampleAlert({
				id: 'a4',
				kind: 'temperature',
				resourceType: 'environment',
				resourceId: 'e1',
				title: 'homelab runs hot',
				detail: 'A sensor stayed above 90 °C for 5 minutes.',
				fields: [
					{ name: 'Environment', value: 'homelab', inline: true },
					{ name: 'Severity', value: 'Critical', inline: true },
					{ name: 'Sensor', value: 'coretemp Package id 0', inline: true },
					{ name: 'Highest', value: '92 °C', inline: true },
					{ name: 'Thresholds', value: 'Warning at 80 °C, critical at 90 °C' }
				]
			})
		]);
		mount(AlertsView, { environmentId: null });
		const table = await screen.findByRole('table', { name: 'Alerts' });
		const row = within(table).getByRole('link', { name: 'homelab runs hot' }).closest('tr')!;
		expect(within(row).getByText('Temperature')).toBeInTheDocument();
		expect(within(row).getByText('coretemp Package id 0')).toBeInTheDocument();
		expect(within(row).getByText('92 °C')).toBeInTheDocument();
		// Long values and what the row shows anyway stay out of the line.
		expect(row).not.toHaveTextContent('Thresholds');
		expect(row).not.toHaveTextContent('Severity');
	});

	it('hides the environment column and filter for one environment', async () => {
		stub(() => [failingDisk]);
		mount(AlertsView, { environmentId: 'e1' });
		const table = await screen.findByRole('table', { name: 'Alerts' });
		expect(alertGets()[0].search.get('environmentId')).toBe('e1');
		expect(within(table).queryByRole('columnheader', { name: 'Environment' })).toBeNull();
		expect(screen.queryByRole('combobox', { name: 'Environment' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Environment' })).toBeNull();
	});

	it('invites the owner to have alerts sent when nothing is active', async () => {
		stub(() => []);
		mount(AlertsView, { environmentId: null, owner: true });
		expect(
			await screen.findByRole('heading', { name: 'No active alerts.' })
		).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Set up notifications' })).toHaveAttribute(
			'href',
			'/settings/notifications'
		);
		expect(screen.queryByRole('button', { name: 'Dismiss all' })).not.toBeInTheDocument();
	});

	it('shows who dismissed an alert and how resolved ones ended', async () => {
		sessionStorage.setItem(
			LIST_FILTERS_PREFIX + 'alerts',
			JSON.stringify({ q: '', values: { state: 'dismissed' } })
		);
		stub((s) =>
			s.get('state') === 'dismissed'
				? [
						{
							...failingDisk,
							dismissed: true,
							dismissedAt: new Date(Date.now() - 2 * 3_600_000).toISOString(),
							dismissedBy: { id: 'u1', name: 'Alex' }
						}
					]
				: [
						sampleAlert({
							id: 'r1',
							state: 'resolved',
							resolution: 'archived',
							resolvedAt: new Date(Date.now() - 3_600_000).toISOString(),
							title: 'edge is offline'
						})
					]
		);
		const { unmount } = mount(AlertsView, { environmentId: null });
		const table = await screen.findByRole('table', { name: 'Alerts' });
		expect(within(table).getByText('Dismissed by Alex 2 hours ago')).toBeInTheDocument();
		expect(within(table).queryByRole('button', { name: /^Dismiss / })).toBeNull();
		unmount();

		sessionStorage.setItem(
			LIST_FILTERS_PREFIX + 'alerts',
			JSON.stringify({ q: '', values: { state: 'resolved' } })
		);
		mount(AlertsView, { environmentId: null });
		const resolved = await screen.findByRole('table', { name: 'Alerts' });
		expect(within(resolved).getByText('Environment archived')).toBeInTheDocument();
		expect(
			within(resolved).getByRole('columnheader', { name: /Resolution/ })
		).toBeInTheDocument();
		expect(alertGets().at(-1)?.search.get('state')).toBe('resolved');
	});
});

describe('AlertMark (#159 System tab)', () => {
	it('opens Alerts filtered to the environment, kind and state', async () => {
		stub(() => []);
		render(AlertMark, { props: { alert: { ...degradedArray, dismissed: true } } });
		const link = screen.getByRole('link', {
			name: 'Alert dismissed: RAID md0 on homelab is degraded'
		});
		expect(link).toHaveAttribute('href', '/notifications?tab=alerts');
		expect(link).toHaveAttribute('title', 'RAID md0 on homelab is degraded');
		link.addEventListener('click', (e) => e.preventDefault());
		link.click();
		expect(JSON.parse(sessionStorage.getItem(LIST_FILTERS_PREFIX + 'alerts') ?? '{}')).toEqual({
			q: '',
			values: { state: 'dismissed', kind: 'raid', environment: 'e1' }
		});
	});

	it('reads "Alert" while it is active', () => {
		render(AlertMark, { props: { alert: failingDisk } });
		expect(
			screen.getByRole('link', { name: 'Alert: Disk /dev/sda on homelab is failing' })
		).toHaveTextContent('Alert');
	});
});
