// The alert thresholds card (Settings → Notifications): the default levels
// checked in words and saved with If-Match, and the environments'
// overrides added, shown and removed through the same PUT.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import { toast } from '$lib/ui';
import QueryHarness from '../../../test/QueryHarness.svelte';
import ThresholdsCard from './ThresholdsCard.svelte';
import type { AlertSettings } from './thresholds';

const json = (body: unknown) =>
	new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });

const settings: AlertSettings = {
	thresholds: {
		temperatureWarning: 80,
		temperatureCritical: 90,
		diskSpaceWarning: 85,
		diskSpaceCritical: 95,
		memoryWarning: 90,
		memoryCritical: 95
	},
	overrides: [{ environmentId: 'e1', temperatureWarning: 70 }],
	revision: 4,
	updatedAt: '2026-09-30T09:00:00Z'
};

let puts: { ifMatch: string | null; body: { overrides: unknown[]; thresholds: unknown } }[] = [];

function stub() {
	puts = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (req: Request) => {
			const url = new URL(req.url);
			if (url.pathname === '/api/v1/environments')
				return json({
					items: [
						{ id: 'e1', name: 'homelab', online: true, actions: [] },
						{ id: 'e2', name: 'edge', online: true, actions: [] },
						{
							id: 'e3',
							name: 'old-lab',
							online: false,
							actions: [],
							archivedAt: '2026-09-01T00:00:00Z'
						}
					]
				});
			if (url.pathname === '/api/v1/alert-settings') {
				if (req.method === 'PUT') {
					const body = await req.json();
					puts.push({ ifMatch: req.headers.get('If-Match'), body });
					return json({ ...settings, ...body, revision: 5 });
				}
				return json(settings);
			}
			return new Response('{}', { status: 404 });
		})
	);
}

function show() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	type P = ComponentProps<typeof ThresholdsCard>;
	return render(QueryHarness<P>, {
		props: { client, component: ThresholdsCard, props: {} as P }
	});
}

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

afterEach(() => {
	vi.unstubAllGlobals();
	toast.clear();
});

describe('Alert thresholds card', () => {
	it('saves changed defaults with If-Match and keeps the overrides', async () => {
		const user = setup();
		stub();
		show();
		const memory = await screen.findByRole('spinbutton', { name: 'Memory Warning (% Used)' });
		expect(memory).toHaveValue(90);
		expect(screen.getByRole('spinbutton', { name: 'Temperature Critical (°C)' })).toHaveValue(
			90
		);
		const save = screen.getByRole('button', { name: 'Save Thresholds' });
		expect(save).toBeDisabled();

		await user.clear(memory);
		await user.type(memory, '80');
		expect(save).toBeEnabled();
		await user.click(save);
		await waitFor(() => expect(puts).toHaveLength(1));
		expect(puts[0].ifMatch).toBe('"4"');
		expect(puts[0].body).toEqual({
			thresholds: { ...settings.thresholds, memoryWarning: 80 },
			overrides: [{ environmentId: 'e1', temperatureWarning: 70 }]
		});
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toContain('Saved alert thresholds')
		);
	});

	it('says what is wrong in words and saves nothing', async () => {
		const user = setup();
		stub();
		show();
		const warning = await screen.findByRole('spinbutton', {
			name: 'Disk Space Warning (% Used)'
		});
		await user.clear(warning);
		await user.type(warning, '96');
		expect(await screen.findByText('Set it below the critical level.')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Save Thresholds' })).toBeDisabled();

		const temperature = screen.getByRole('spinbutton', { name: 'Temperature Critical (°C)' });
		await user.clear(temperature);
		await user.type(temperature, '200');
		expect(await screen.findByText('Enter a whole number from 0 to 150.')).toBeInTheDocument();

		// A default that would break an override is refused before saving:
		// homelab's warning (70 °C) would not be below a critical 65 °C.
		await user.clear(warning);
		await user.type(warning, '85');
		const temperatureWarning = screen.getByRole('spinbutton', {
			name: 'Temperature Warning (°C)'
		});
		await user.clear(temperatureWarning);
		await user.type(temperatureWarning, '50');
		await user.clear(temperature);
		await user.type(temperature, '65');
		expect(
			await screen.findByText(/Change or remove the override of homelab first/)
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Save Thresholds' })).toBeDisabled();
		expect(puts).toEqual([]);
	});

	it('lists the overrides and adds one for an environment without one', async () => {
		const user = setup();
		stub();
		show();
		const table = await screen.findByRole('table', { name: 'Environment Overrides' });
		const row = within(table).getByText('homelab').closest('tr')!;
		expect(row).toHaveTextContent('70 °C / Default');
		expect(within(row).getAllByText('Default')).toHaveLength(2);

		await user.click(screen.getByRole('button', { name: 'Add Override' }));
		const dialog = await screen.findByRole('dialog', { name: 'Override Thresholds' });
		expect(dialog).toHaveTextContent(
			'Leave a level empty to use the default; 0 turns it off there.'
		);
		// The default as placeholder.
		expect(
			within(dialog).getByRole('spinbutton', { name: 'Temperature Warning (°C)' })
		).toHaveAttribute('placeholder', '80');
		const env = within(dialog).getByRole('combobox', { name: /^Environment/ });
		await user.click(env);
		// Only active environments without an override.
		expect(await screen.findByRole('option', { name: 'edge' })).toBeInTheDocument();
		expect(screen.queryByRole('option', { name: 'homelab' })).toBeNull();
		expect(screen.queryByRole('option', { name: 'old-lab' })).toBeNull();
		await user.click(screen.getByRole('option', { name: 'edge' }));
		await user.type(
			within(dialog).getByRole('spinbutton', { name: 'Memory Critical (% Used)' }),
			'99'
		);
		await user.click(within(dialog).getByRole('button', { name: 'Add Override' }));
		await waitFor(() => expect(puts).toHaveLength(1));
		expect(puts[0].ifMatch).toBe('"4"');
		expect(puts[0].body).toEqual({
			thresholds: settings.thresholds,
			overrides: [
				{ environmentId: 'e1', temperatureWarning: 70 },
				{ environmentId: 'e2', memoryCritical: 99 }
			]
		});
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toContain('Added an override for edge')
		);
	});

	it('needs an environment before adding an override', async () => {
		const user = setup();
		stub();
		show();
		await user.click(await screen.findByRole('button', { name: 'Add Override' }));
		const dialog = await screen.findByRole('dialog', { name: 'Override Thresholds' });
		await user.click(within(dialog).getByRole('button', { name: 'Add Override' }));
		expect(await within(dialog).findByText('Choose an environment.')).toBeInTheDocument();
		expect(puts).toEqual([]);
	});

	it('removes an override after confirming', async () => {
		const user = setup();
		stub();
		show();
		await screen.findByRole('table', { name: 'Environment Overrides' });
		await user.click(screen.getByRole('button', { name: 'Actions for homelab' }));
		expect((await screen.findAllByRole('menuitem')).map((i) => i.textContent?.trim())).toEqual([
			'Edit',
			'Remove'
		]);
		await user.click(screen.getByRole('menuitem', { name: 'Remove' }));
		const confirm = await screen.findByRole('alertdialog', {
			name: 'Remove the override of homelab?'
		});
		await user.click(within(confirm).getByRole('button', { name: 'Remove Override' }));
		await waitFor(() => expect(puts).toHaveLength(1));
		expect(puts[0].body).toEqual({ thresholds: settings.thresholds, overrides: [] });
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toContain('Removed the override of homelab')
		);
	});

	it('refetches after a failed removal, so a retry sends the current revision', async () => {
		const user = setup();
		stub();
		// Someone else saved the thresholds meanwhile: the first PUT is
		// refused (412) and the settings are at revision 6 now.
		let revision = 4;
		let gets = 0;
		const base = globalThis.fetch as (req: Request) => Promise<Response>;
		vi.stubGlobal(
			'fetch',
			vi.fn(async (req: Request) => {
				const url = new URL(req.url);
				if (url.pathname === '/api/v1/alert-settings') {
					if (req.method === 'PUT') {
						puts.push({ ifMatch: req.headers.get('If-Match'), body: await req.json() });
						if (puts.length === 1) {
							revision = 6;
							return new Response(
								JSON.stringify({
									status: 412,
									code: 'precondition_failed',
									detail: 'the resource was changed since you loaded it'
								}),
								{
									status: 412,
									headers: { 'Content-Type': 'application/problem+json' }
								}
							);
						}
						return json({ ...settings, overrides: [], revision: revision + 1 });
					}
					gets++;
					return json({ ...settings, revision });
				}
				return base(req);
			})
		);
		show();
		await screen.findByRole('table', { name: 'Environment Overrides' });
		await user.click(screen.getByRole('button', { name: 'Actions for homelab' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Remove' }));
		const confirm = await screen.findByRole('alertdialog', {
			name: 'Remove the override of homelab?'
		});
		await user.click(within(confirm).getByRole('button', { name: 'Remove Override' }));
		await waitFor(() => expect(puts).toHaveLength(1));
		expect(puts[0].ifMatch).toBe('"4"');
		// The failure refetched the settings: the retry from the same dialog
		// carries the current revision.
		await waitFor(() => expect(gets).toBe(2));
		const retry = within(confirm).getByRole('button', { name: 'Remove Override' });
		await waitFor(() => expect(retry).toBeEnabled());
		await user.click(retry);
		await waitFor(() => expect(puts).toHaveLength(2));
		expect(puts[1].ifMatch).toBe('"6"');
	});
});
