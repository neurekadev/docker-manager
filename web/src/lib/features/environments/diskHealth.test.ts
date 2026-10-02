// Disk health and RAID cards (#143): the notices that replace the list,
// one row per disk with its issues in words, "Check disks now" and "Check
// RAID now" calling the check route and toasting the result (also when
// the agent is still reading and the result arrives later).
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Environment } from '$lib/api/client';
import { toast } from '$lib/ui';
import QueryHarness from '../../../test/QueryHarness.svelte';
import DiskHealthCard from './DiskHealthCard.svelte';
import RaidCard from './RaidCard.svelte';
import { sampleAlert } from '$lib/features/alerts/test/samples';
import type { DiskDevice, DiskHealth, RaidHealth } from './diskHealth';

const json = (body: unknown, status = 200) =>
	new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let requests: { method: string; path: string; body: unknown }[] = [];
function stubApi(answer: (body: { scope: string }) => Response) {
	requests = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: RequestInfo, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const url = new URL(req.url);
			const body = req.method === 'POST' ? await req.json() : undefined;
			requests.push({ method: req.method, path: url.pathname, body });
			if (
				req.method === 'POST' &&
				url.pathname === '/api/v1/environments/e1/disk-health/checks'
			)
				return answer(body);
			return json({ code: 'not_found', message: 'not found' }, 404);
		})
	);
}

afterEach(() => {
	vi.unstubAllGlobals();
	toast.clear();
});

const env: Environment = {
	id: 'e1',
	name: 'homelab',
	online: true,
	status: 'active',
	view: 'full',
	revision: 7,
	actions: ['environment.read', 'environment.system.read']
};
const now = new Date('2026-09-29T12:00:00Z');

const sda: DiskDevice = {
	name: '/dev/sda',
	type: 'sat',
	protocol: 'ata',
	model: 'WDC WD40EFZX-68AWUN0',
	serial: 'WD-WX12D3456789',
	firmware: '81.00B81',
	smartSupported: true,
	temperatureC: 36,
	powerOnHours: (3 * 365 + 41) * 24,
	state: 'ok',
	readAt: '2026-09-29T11:48:00Z'
};
const sdb: DiskDevice = {
	...sda,
	name: '/dev/sdb',
	model: 'TOSHIBA MG08ACA16TE',
	serial: 'X0A0A0A0FVGG',
	reallocatedSectors: 8,
	state: 'warning'
};

const report = (p: Partial<DiskHealth> = {}): DiskHealth => ({
	status: 'ok',
	checking: false,
	checkedAt: '2026-09-29T11:48:00Z',
	devices: [sda, sdb],
	...p
});
const noArrays: RaidHealth = { status: 'ok', readAt: '2026-09-29T11:59:00Z', arrays: [] };

function mount<P extends Record<string, unknown>>(component: Component<P>, props: P) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(QueryHarness<P>, { props: { client, component, props } });
}

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

describe('DiskHealthCard', () => {
	it('lists the disks, problems first, with issues in words', () => {
		mount(DiskHealthCard, { env, health: report(), raid: noArrays, online: true, now });
		const table = screen.getByRole('table', { name: 'Disks of homelab' });
		const rows = within(table).getAllByRole('row').slice(1);
		expect(rows).toHaveLength(2);
		expect(within(rows[0]).getByText('/dev/sdb')).toBeInTheDocument();
		// Every disk shows the disk tile, decorative.
		for (const row of rows)
			expect(row.querySelector('[data-color="slate"]')).toHaveAttribute(
				'aria-hidden',
				'true'
			);
		expect(within(rows[0]).getByText('Warning')).toBeInTheDocument();
		expect(within(rows[0]).getByText('8 reallocated sectors')).toBeInTheDocument();
		expect(within(rows[1]).getByText('Healthy')).toBeInTheDocument();
		expect(within(rows[1]).getByText('36 °C')).toBeInTheDocument();
		expect(within(rows[1]).getByText('3 y 41 d')).toBeInTheDocument();
		expect(within(rows[1]).getByText('None')).toBeInTheDocument();
		expect(screen.getByText('2 disks, 1 needs attention')).toBeInTheDocument();
		expect(screen.getByText('Last checked 12 minutes ago')).toHaveAttribute('title');
		expect(screen.getByText('No RAID arrays found')).toBeInTheDocument();
		// Serial numbers wait under "Advanced".
		expect(screen.getByText('Advanced')).toBeInTheDocument();
		const details = screen.getByRole('table', {
			name: 'Disk details of homelab',
			hidden: true
		});
		expect(within(details).getByText('WD-WX12D3456789')).toBeInTheDocument();
	});

	it('opens a disk’s full SMART data', async () => {
		const user = setup();
		const full: DiskDevice = {
			...sda,
			passed: true,
			temperatureLimitC: 70,
			percentageUsed: 93,
			attributes: [
				{
					id: 5,
					name: 'Reallocated_Sector_Ct',
					value: 100,
					worst: 100,
					threshold: 10,
					raw: 0,
					prefailure: true
				},
				{
					id: 194,
					name: 'Temperature_Celsius',
					value: 114,
					worst: 101,
					threshold: 0,
					raw: 36,
					rawText: '36 (Min/Max 20/49)',
					whenFailed: 'past'
				}
			],
			values: [{ key: 'power_cycle_count', value: 41 }]
		};
		mount(DiskHealthCard, {
			env,
			health: report({ devices: [full] }),
			raid: noArrays,
			online: true,
			now
		});
		await user.click(screen.getByRole('button', { name: 'Details of /dev/sda' }));
		const dialog = await screen.findByRole('dialog', { name: '/dev/sda' });
		expect(within(dialog).getByText('WD-WX12D3456789')).toBeInTheDocument();
		expect(within(dialog).getByText('Passed')).toBeInTheDocument();
		const attrs = within(dialog).getByRole('table', { name: 'SMART attributes of /dev/sda' });
		const rows = within(attrs).getAllByRole('row').slice(1);
		expect(rows).toHaveLength(2);
		expect(within(rows[0]).getByText('Reallocated_Sector_Ct')).toBeInTheDocument();
		expect(within(rows[0]).getByText('Pre-fail')).toBeInTheDocument();
		expect(within(rows[1]).getByText('36 (Min/Max 20/49)')).toBeInTheDocument();
		// Marks: OK in green for a pre-fail attribute, a warning for one
		// that failed in the past (#210).
		expect(within(rows[0]).getByText('OK').closest('[data-tone]')).toHaveAttribute(
			'data-tone',
			'ok'
		);
		expect(
			within(rows[1]).getByText('Failed in the past').closest('[data-tone]')
		).toHaveAttribute('data-tone', 'warn');
		expect(within(dialog).getByText('Passed').closest('[data-tone]')).toHaveAttribute(
			'data-tone',
			'ok'
		);
		// The temperature against the disk's own limit, and its wear (#212).
		expect(
			within(dialog).getByText('36 °C (limit 70 °C)').closest('[data-tone]')
		).toHaveAttribute('data-tone', 'ok');
		expect(within(dialog).getByText('93% used').closest('[data-tone]')).toHaveAttribute(
			'data-tone',
			'warn'
		);
		const values = within(dialog).getByRole('table', { name: 'Health values of /dev/sda' });
		expect(within(values).getByText('Power cycles')).toBeInTheDocument();
		expect(within(values).getByText('41')).toBeInTheDocument();
	});

	it('says when an older agent sent no detailed SMART values', async () => {
		const user = setup();
		mount(DiskHealthCard, { env, health: report(), raid: noArrays, online: true, now });
		await user.click(screen.getByRole('button', { name: 'Details of /dev/sdb' }));
		const dialog = await screen.findByRole('dialog', { name: '/dev/sdb' });
		expect(
			within(dialog).getByText(
				'The disk reported no detailed SMART values. Agents older than this view don’t send them; update the agent if it is older.'
			)
		).toBeInTheDocument();
	});

	it.each([
		['no_access', 'Docker Manager can’t read this server’s disks.'],
		['disabled', 'Disk health is turned off for this agent.'],
		['agent_outdated', 'Update the agent to see disk health.']
	] as const)('shows the %s notice instead of the list', (status, title) => {
		mount(DiskHealthCard, {
			env,
			health: report({ status, devices: [] }),
			raid: noArrays,
			online: true,
			now
		});
		expect(screen.getByText(title)).toBeInTheDocument();
		expect(screen.queryByRole('table', { name: 'Disks of homelab' })).toBeNull();
		if (status === 'no_access') {
			expect(screen.getByText('privileged: true')).toBeInTheDocument();
			const link = screen.getByRole('link', { name: /How to give the agent access/ });
			expect(link).toHaveAttribute('target', '_blank');
			expect(link).toHaveAttribute('rel', 'noopener noreferrer');
		}
		expect(screen.queryByRole('button', { name: 'Check disks now' })).toBeNull();
	});

	it('says when the disks report no SMART data', () => {
		mount(DiskHealthCard, {
			env,
			health: report({ devices: [{ ...sda, state: 'error', errorCode: 'unsupported' }] }),
			online: true,
			now
		});
		expect(
			screen.getByText(
				'This server’s disks don’t report SMART data (for example virtual disks).'
			)
		).toBeInTheDocument();
		expect(screen.queryByRole('table', { name: 'Disks of homelab' })).toBeNull();
	});

	it('lists disks behind one controller path as separate rows', () => {
		const slot0: DiskDevice = { ...sda, name: '/dev/bus/0', type: 'megaraid,0', serial: 'S0' };
		const slot1: DiskDevice = { ...sda, name: '/dev/bus/0', type: 'megaraid,1', serial: 'S1' };
		mount(DiskHealthCard, {
			env,
			health: report({ devices: [slot1, slot0] }),
			online: true,
			now
		});
		const table = screen.getByRole('table', { name: 'Disks of homelab' });
		const rows = within(table).getAllByRole('row').slice(1);
		expect(rows).toHaveLength(2);
		expect(within(rows[0]).getByText('/dev/bus/0 (megaraid,0)')).toBeInTheDocument();
		expect(within(rows[1]).getByText('/dev/bus/0 (megaraid,1)')).toBeInTheDocument();
	});

	it('says when a finished scan found no disks', () => {
		mount(DiskHealthCard, { env, health: report({ devices: [] }), online: true, now });
		expect(
			screen.getByText('No disks with SMART data were found on this server.')
		).toBeInTheDocument();
	});

	it('hides the check while the environment is offline', () => {
		mount(DiskHealthCard, { env, health: report(), online: false, now });
		expect(screen.queryByRole('button', { name: 'Check disks now' })).toBeNull();
	});

	it('checks the disks and toasts the result', async () => {
		const user = setup();
		stubApi(() =>
			json({
				environmentId: 'e1',
				scope: 'smart',
				diskHealth: report({ checkedAt: '2026-09-29T12:00:00Z' }),
				raid: noArrays
			})
		);
		mount(DiskHealthCard, { env, health: report(), raid: noArrays, online: true, now });
		await user.click(screen.getByRole('button', { name: 'Check disks now' }));
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toContain('Checked 2 disks on homelab')
		);
		expect(requests).toContainEqual({
			method: 'POST',
			path: '/api/v1/environments/e1/disk-health/checks',
			body: { scope: 'smart' }
		});
	});

	it('waits for a long check and toasts when the new report arrives', async () => {
		const user = setup();
		stubApi(() =>
			json({
				environmentId: 'e1',
				scope: 'smart',
				diskHealth: report({ checking: true }),
				raid: noArrays
			})
		);
		const view = mount(DiskHealthCard, {
			env,
			health: report(),
			raid: noArrays,
			online: true,
			now
		});
		// New props of the card inside QueryHarness: testing-library unwraps
		// a top-level `props` key (its deprecated form), so the harness's own
		// `props` is wrapped once more.
		const update = (props: Record<string, unknown>) =>
			view.rerender({ props: { props } } as unknown as Parameters<typeof view.rerender>[0]);
		await user.click(screen.getByRole('button', { name: 'Check disks now' }));
		await waitFor(() => expect(requests).toHaveLength(1));
		expect(toast.items).toHaveLength(0);
		// The live stream refreshes the system information: still reading…
		await update({
			env,
			health: report({ checking: true }),
			raid: noArrays,
			online: true,
			now
		});
		expect(screen.getByRole('button', { name: 'Check disks now' })).toHaveAttribute(
			'aria-busy',
			'true'
		);
		// …then done.
		await update({
			env,
			health: report({ checkedAt: '2026-09-29T12:01:00Z' }),
			raid: noArrays,
			online: true,
			now
		});
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toContain('Checked 2 disks on homelab')
		);
	});

	it('says why a check was refused', async () => {
		const user = setup();
		stubApi(() =>
			json(
				{
					code: 'rate_limited',
					message: 'checked moments ago',
					details: [],
					requestId: 'r',
					retryable: true
				},
				429
			)
		);
		mount(DiskHealthCard, { env, health: report(), online: true, now });
		await user.click(screen.getByRole('button', { name: 'Check disks now' }));
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toContain('Couldn’t check the disks on homelab')
		);
	});
});

describe('RaidCard', () => {
	const raid: RaidHealth = {
		status: 'ok',
		readAt: '2026-09-29T11:59:00Z',
		arrays: [
			{
				kind: 'md',
				name: 'md0',
				level: 'raid1',
				state: 'healthy',
				devices: 2,
				active: 2,
				members: [
					{ name: 'sda1', slot: 0, state: 'active' },
					{ name: 'sdb1', slot: 1, state: 'active' }
				]
			},
			{
				kind: 'md',
				name: 'md1',
				level: 'raid5',
				state: 'rebuilding',
				devices: 3,
				active: 2,
				action: 'recovery',
				progress: 17.3,
				finishSeconds: 4686,
				members: [
					{ name: 'sdc1', slot: 0, state: 'active' },
					{ name: 'sdd1', slot: 1, state: 'failed' },
					{ name: 'sde1', slot: 2, state: 'active' }
				]
			},
			{ kind: 'zfs', name: 'tank', state: 'healthy', health: 'ONLINE', members: [] }
		]
	};

	it('shows each array with its state, members and rebuild progress', () => {
		mount(RaidCard, { env, raid, online: true, now });
		const table = screen.getByRole('table', { name: 'RAID arrays of homelab' });
		const rows = within(table).getAllByRole('row').slice(1);
		expect(within(rows[0]).getByText('/dev/md1')).toBeInTheDocument();
		// Every array shows the RAID tile, decorative.
		for (const row of rows)
			expect(row.querySelector('[data-color="indigo"]')).toHaveAttribute(
				'aria-hidden',
				'true'
			);
		expect(within(rows[0]).getByText('Rebuilding')).toBeInTheDocument();
		expect(within(rows[0]).getByText('RAID 5')).toBeInTheDocument();
		expect(within(rows[0]).getByText('/dev/sdd1 failed')).toHaveClass('failed');
		// Active members are only in the tooltip; the cell names what is wrong.
		expect(within(rows[0]).queryByText('/dev/sdc1')).toBeNull();
		expect(within(rows[0]).getByText('2 of 3 disks working')).toBeInTheDocument();
		expect(
			within(rows[0]).getByText('Rebuilding 17.3%, about 1 h 18 min left')
		).toBeInTheDocument();
		expect(
			within(rows[0]).getByRole('progressbar', { name: '/dev/md1 progress' })
		).toHaveAttribute('aria-valuenow', '17.3');
		expect(within(rows[2]).getByText('ZFS pool')).toBeInTheDocument();
	});

	it('opens an array’s details with its members and their disks', async () => {
		const user = setup();
		const sdc: DiskDevice = { ...sda, name: '/dev/sdc', state: 'warning' };
		const details: RaidHealth = {
			...raid,
			arrays: [
				{
					...raid.arrays[1],
					metadata: '1.2',
					chunkBytes: 512 * 1024,
					layout: 'algorithm 2',
					bitmap: true,
					bitmapChunkBytes: 64 * 1024 * 1024,
					sizeBytes: 2 * 1024 ** 4
				}
			]
		};
		mount(RaidCard, { env, raid: details, devices: [sdc], online: true, now });
		await user.click(screen.getByRole('button', { name: 'Details of /dev/md1' }));
		const dialog = await screen.findByRole('dialog', { name: '/dev/md1' });
		expect(within(dialog).getByText('1.2')).toBeInTheDocument();
		expect(within(dialog).getByText('512 KB')).toBeInTheDocument();
		expect(within(dialog).getByText('Left-symmetric (algorithm 2)')).toBeInTheDocument();
		expect(within(dialog).getByText('Yes, 64 MB chunks')).toBeInTheDocument();
		expect(within(dialog).getByText('2 TB')).toBeInTheDocument();
		const members = within(dialog).getByRole('table', { name: 'Members of /dev/md1' });
		const rows = within(members).getAllByRole('row').slice(1);
		// Failed members first; each with the health of the disk it lives on.
		expect(within(rows[0]).getByText('/dev/sdd1')).toBeInTheDocument();
		expect(within(rows[0]).getByText('Failed')).toBeInTheDocument();
		expect(within(rows[1]).getByText('/dev/sdc1')).toBeInTheDocument();
		expect(within(rows[1]).getByText('Warning')).toBeInTheDocument();
		expect(within(rows[1]).getByText('/dev/sdc')).toBeInTheDocument();
	});

	it('checks RAID and toasts the result', async () => {
		const user = setup();
		stubApi(() => json({ environmentId: 'e1', scope: 'raid', diskHealth: report(), raid }));
		mount(RaidCard, { env, raid, online: true, now });
		await user.click(screen.getByRole('button', { name: 'Check RAID now' }));
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toContain('Checked 3 arrays on homelab')
		);
		expect(requests[0]).toMatchObject({ method: 'POST', body: { scope: 'raid' } });
	});
});

describe('alert marks (#159)', () => {
	it('marks a disk with a firing alert, linking to Alerts', () => {
		const diskAlert = sampleAlert({
			id: 'a1',
			title: 'Disk /dev/sdb on homelab needs attention',
			facts: { device: '/dev/sdb', deviceType: 'sat', state: 'warning' }
		});
		const other = sampleAlert({
			id: 'a9',
			title: 'Disk /dev/sda (nvme) on homelab is failing',
			facts: { device: '/dev/sda', deviceType: 'nvme' }
		});
		mount(DiskHealthCard, {
			env,
			health: report(),
			raid: noArrays,
			online: true,
			alerts: [diskAlert, other],
			now
		});
		const table = screen.getByRole('table', { name: 'Disks of homelab' });
		const [sdbRow, sdaRow] = within(table).getAllByRole('row').slice(1);
		const mark = within(sdbRow).getByRole('link', {
			name: 'Alert: Disk /dev/sdb on homelab needs attention'
		});
		expect(mark).toHaveAttribute('href', '/notifications?tab=alerts');
		// An alert of another disk type on the same path does not mark this disk.
		expect(within(sdaRow).queryByRole('link')).toBeNull();
	});

	it('marks a dismissed alert of a ZFS pool as dismissed, not an md array of that name', () => {
		const raid: RaidHealth = {
			status: 'ok',
			readAt: '2026-09-29T11:59:00Z',
			arrays: [
				{ kind: 'zfs', name: 'tank', state: 'degraded', health: 'DEGRADED', members: [] },
				{
					kind: 'md',
					name: 'tank',
					level: 'raid1',
					state: 'healthy',
					devices: 2,
					active: 2,
					members: []
				}
			]
		};
		const poolAlert = sampleAlert({
			id: 'z1',
			kind: 'raid',
			resourceType: 'zfs_pool',
			resourceId: 'tank',
			title: 'ZFS pool tank on homelab is degraded',
			facts: { pool: 'tank', arrayKind: 'zfs', health: 'DEGRADED' },
			dismissed: true
		});
		mount(RaidCard, { env, raid, online: true, alerts: [poolAlert], now });
		const table = screen.getByRole('table', { name: 'RAID arrays of homelab' });
		expect(
			within(table).getAllByRole('link', {
				name: 'Alert dismissed: ZFS pool tank on homelab is degraded'
			})
		).toHaveLength(1);
	});
});
