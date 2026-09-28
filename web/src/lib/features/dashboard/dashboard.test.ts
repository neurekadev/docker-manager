import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Schema } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import EnvironmentCard from './EnvironmentCard.svelte';

vi.mock('$lib/lazy', async (orig) => ({
	...(await orig<typeof import('$lib/lazy')>()),
	mountSparkline: vi.fn(async () => ({ update() {}, destroy() {} }))
}));

const json = (body: unknown) =>
	new Response(JSON.stringify(body), {
		status: 200,
		headers: { 'Content-Type': 'application/json' }
	});
let urls: string[] = [];

function stub() {
	urls = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			urls.push(u.pathname + u.search);
			if (u.pathname.endsWith('/system'))
				return json({
					environmentId: 'e3',
					online: false,
					engine: {
						id: 'x',
						version: '27.3.1',
						apiVersion: '1.47',
						os: 'linux',
						arch: 'arm64',
						rootless: false
					},
					commands: [],
					features: [],
					requests: [],
					streams: [],
					roots: [],
					diagnostics: []
				});
			if (u.pathname.endsWith('/metrics'))
				return json({
					environmentId: 'e3',
					from: '2026-09-25T11:30:00Z',
					to: '2026-09-25T12:00:00Z',
					stepSeconds: 60,
					resolution: '1m',
					timestamps: [
						'2026-09-25T11:30:00Z',
						'2026-09-25T11:31:00Z',
						'2026-09-25T11:32:00Z'
					],
					series: [{ key: 'cpu.percent', unit: 'percent', values: [2, null, null] }],
					skewCorrected: false,
					incomplete: false,
					online: false
				});
			return new Response('{}', { status: 404 });
		})
	);
}

function mount<P extends Record<string, unknown>>(component: Component<P>, props: P) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(QueryHarness<P>, { props: { client, component, props } });
}

afterEach(() => vi.unstubAllGlobals());

const edge: Schema<'OverviewEnvironment'> = {
	id: 'e3',
	name: 'edge',
	online: false,
	view: 'full',
	actions: ['environment.read', 'environment.metrics.read', 'environment.system.read'],
	usage: {
		sampledAt: '2026-09-25T11:31:00Z',
		cpuPercent: 1.4,
		memoryUsedBytes: 334_000_000,
		memoryTotalBytes: 4 * 1024 ** 3
	},
	docker: {
		containers: 3,
		containersRunning: 1,
		containersPaused: 0,
		containersStopped: 2,
		images: 4,
		volumes: -1,
		networks: 2
	}
};

describe('EnvironmentCard (#5 dashboard)', () => {
	it('shows an offline environment as offline with its last known values', async () => {
		stub();
		mount(EnvironmentCard, {
			env: edge,
			since: '2026-09-25T11:32:00Z',
			stacks: 2,
			undeployed: 1,
			updates: 3,
			now: new Date('2026-09-25T12:02:00Z')
		});
		const card = screen.getByRole('article', { name: 'edge' });
		expect(within(card).getByText('Offline')).toBeInTheDocument(); // the word, not colour alone
		expect(card).toHaveTextContent('Offline since 30 minutes ago. Values are the last known.');
		expect(card).toHaveTextContent('1.4%');
		expect(card).toHaveTextContent('319 MB / 4 GB');
		expect(card).toHaveTextContent('Containers 1 / 3');
		expect(card).toHaveTextContent('Volumes —'); // -1: unknown, never shown as a number
		expect(
			within(card).getByRole('link', { name: /1 stack has undeployed changes/ })
		).toHaveAttribute('href', '/stacks');
		expect(within(card).getByRole('link', { name: /3 updates available/ })).toHaveAttribute(
			'href',
			'/updates'
		);
		expect(await within(card).findByText(/Docker 27\.3\.1/)).toBeInTheDocument();
		expect(
			await within(card).findByText('CPU of edge, last 30 minutes, with gaps without samples')
		).toBeInTheDocument();
		await waitFor(() =>
			expect(urls.some((u) => u.startsWith('/api/v1/environments/e3/metrics?'))).toBe(true)
		);
		const q = new URLSearchParams(urls.find((u) => u.includes('/metrics'))!.split('?')[1]);
		expect(q.get('series')).toBe('cpu.percent,memory.used_bytes');
		expect(q.get('stepSeconds')).toBe('10');
	});

	it('asks only for what the user may read', () => {
		stub();
		mount(EnvironmentCard, {
			env: {
				...edge,
				online: true,
				actions: ['environment.read'],
				usage: undefined,
				docker: undefined
			}
		});
		const card = screen.getByRole('article', { name: 'edge' });
		expect(within(card).getByText('Online')).toBeInTheDocument();
		expect(card).not.toHaveTextContent('CPU');
		expect(card).toHaveTextContent('Engine not reported yet');
		expect(urls).toEqual([]);
	});
});
