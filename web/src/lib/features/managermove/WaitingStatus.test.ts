// The new server's status page (waiting mode, public): built from GET
// /api/v1/move/status alone; the current step highlighted, done with where
// to point DNS, a refused copy with its recovery.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import WaitingStatus from './WaitingStatus.svelte';
import type { MoveStatus } from './model';

let status: MoveStatus;
let requested: string[] = [];

function base(p: Partial<MoveStatus> = {}): MoveStatus {
	return {
		phase: 'waiting',
		oldManager: 'http://192.168.1.10:8080',
		stacksMoved: 0,
		stacksTotal: 0,
		jobsRunning: 0,
		bytesReceived: 0,
		bytesTotal: 0,
		oldManagerConfirmed: false,
		...p
	};
}

beforeEach(() => {
	requested = [];
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const path = new URL(req.url).pathname;
		requested.push(path);
		if (path === '/api/v1/move/status')
			return new Response(JSON.stringify(status), {
				status: 200,
				headers: { 'Content-Type': 'application/json' }
			});
		// Waiting mode answers everything else with 503.
		return new Response(
			JSON.stringify({
				code: 'manager_move_waiting',
				message: 'waiting',
				details: [],
				requestId: 'r',
				retryable: true
			}),
			{ status: 503, headers: { 'Content-Type': 'application/json' } }
		);
	});
});
afterEach(() => vi.unstubAllGlobals());

function page() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: WaitingStatus as unknown as Component<Record<string, unknown>>,
			props: {}
		}
	});
}

describe('WaitingStatus', () => {
	it('shows the steps with the apps moving highlighted', async () => {
		status = base({
			oldState: 'moving',
			stacksMoved: 2,
			stacksTotal: 5,
			currentStack: 'immich'
		});
		page();

		expect(
			await screen.findByRole('heading', { level: 1, name: 'Moving Docker Manager Here' })
		).toBeInTheDocument();
		const steps = await screen.findByRole('list', { name: 'Steps of the Move' });
		const items = within(steps).getAllByRole('listitem');
		expect(items.map((li) => li.textContent?.trim())).toEqual([
			'Waiting for the Old Server: done',
			'Moving Your Apps (2 of 5, now: immich): in progress',
			'Finishing Running Jobs: not started',
			'Copying Docker Manager: not started',
			'Checking the Copy: not started',
			'Restarting: not started',
			'Done: not started'
		]);
		expect(items[1]).toHaveAttribute('aria-current', 'step');
		expect(items[0]).not.toHaveAttribute('aria-current');
		expect(
			screen.getByText('From http://192.168.1.10:8080. This page updates by itself.')
		).toBeInTheDocument();
		// Built from the status alone: no session, no setup status.
		expect(requested.every((p) => p === '/api/v1/move/status')).toBe(true);
	});

	it('says where to point DNS once the move is complete', async () => {
		status = base({ phase: 'complete', publicUrl: 'https://docker.example.com' });
		page();

		expect(await screen.findByText('Docker Manager now runs here.')).toBeInTheDocument();
		expect(
			screen.getByText(
				'Point DNS for docker.example.com at this server, then sign in there as usual. You can remove DOCKER_MANAGER_MOVE_FROM and DOCKER_MANAGER_MOVE_CODE from the .env.'
			)
		).toBeInTheDocument();
		const items = within(screen.getByRole('list', { name: 'Steps of the Move' })).getAllByRole(
			'listitem'
		);
		expect(items.every((li) => li.getAttribute('aria-current') === null)).toBe(true);
	});

	it('shows a refused copy with what to do', async () => {
		status = base({
			phase: 'failed',
			errorCode: 'manager_move_schema_incompatible',
			recovery: 'Update Docker Manager on this server, then restart it.'
		});
		page();

		expect(
			await screen.findByText('This Docker Manager is older than the old one')
		).toBeInTheDocument();
		expect(
			screen.getByText('Update Docker Manager on this server, then restart it.')
		).toBeInTheDocument();
		const items = within(screen.getByRole('list', { name: 'Steps of the Move' })).getAllByRole(
			'listitem'
		);
		expect(items[4]).toHaveAttribute('aria-current', 'step');
		expect(items[4]).toHaveTextContent('Checking the Copy: failed');
	});

	it('says what to do on the old server while the move waits for it', async () => {
		status = base({ oldState: 'open' });
		page();

		expect(await screen.findByText('Ready When You Are')).toBeInTheDocument();
		expect(
			screen.getByText('On the old server, press Move Everything to start.')
		).toBeInTheDocument();
	});
});
