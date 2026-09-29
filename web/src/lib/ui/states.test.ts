import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { ApiRequestError } from '$lib/api/client';
import { JobWatcher } from '$lib/api/jobs.svelte';
import { demoJob, demoJobClient, demoJobEvents, ScriptedEventSource } from '$lib/design/demo';
import DeniedState from './DeniedState.svelte';
import ErrorState from './ErrorState.svelte';
import JobProgress from './JobProgress.svelte';
import OfflineEnvironment from './OfflineEnvironment.svelte';
import SecretReveal from './SecretReveal.svelte';
import StepWizard from './StepWizard.svelte';
import Toaster from './Toaster.svelte';
import { Toasts } from './toast.svelte';
import { Notices } from '$lib/shell/notices.svelte';
import { createRawSnippet } from 'svelte';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

describe('ErrorState', () => {
	it('shows message, code and request ID from the API error, and Retry when it may help', async () => {
		const user = setup();
		const retry = vi.fn();
		render(ErrorState, {
			props: {
				title: 'Silo was not deployed.',
				error: new ApiRequestError('x', 503, {
					code: 'environment_offline',
					message: "Silo can't be deployed because homelab is offline",
					requestId: 'req-42',
					retryable: true,
					details: []
				}),
				onretry: retry
			}
		});
		const alert = screen.getByRole('alert');
		expect(alert).toHaveTextContent('Silo was not deployed.');
		expect(alert).toHaveTextContent("Silo can't be deployed because homelab is offline.");
		// Code and request ID wait behind "Details".
		expect(alert).not.toHaveTextContent('environment_offline');
		const details = screen.getByRole('button', { name: 'Details' });
		expect(details).toHaveAttribute('aria-expanded', 'false');
		await user.click(details);
		expect(details).toHaveAttribute('aria-expanded', 'true');
		expect(alert).toHaveTextContent('environment_offline');
		expect(alert).toHaveTextContent('req-42');
		await user.click(screen.getByRole('button', { name: 'Copy request ID' }));
		expect(await navigator.clipboard.readText()).toBe('req-42');
		await user.click(screen.getByRole('button', { name: 'Retry' }));
		expect(retry).toHaveBeenCalled();
	});

	it('offers no Retry for final errors', () => {
		render(ErrorState, {
			props: {
				error: new ApiRequestError('x', 404, {
					code: 'not_found',
					message: 'stack not found',
					requestId: 'r',
					retryable: false,
					details: []
				}),
				onretry: () => {}
			}
		});
		expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
		// Without a title the message leads.
		expect(screen.getByRole('alert').querySelector('.title')).toHaveTextContent(
			'Stack not found.'
		);
	});

	it('has no Details without a code or request ID', () => {
		render(ErrorState, { props: { error: new Error('the file is too large'), bare: true } });
		expect(screen.getByRole('alert')).toHaveTextContent('The file is too large.');
		expect(screen.queryByRole('button', { name: 'Details' })).toBeNull();
	});
});

describe('DeniedState and offline banner', () => {
	it('tells a Restricted user what to do, calmly', () => {
		render(DeniedState, { props: { level: 1 } });
		expect(
			screen.getByRole('heading', {
				level: 1,
				name: "You don't have access to anything yet."
			})
		).toBeInTheDocument();
		expect(
			screen.getByText('Ask the owner of this Docker Manager to grant access.')
		).toBeInTheDocument();
	});

	it('says offline in words, with the time', () => {
		render(OfflineEnvironment, {
			props: {
				name: 'edge',
				since: '2026-09-25T09:00:00Z',
				now: new Date('2026-09-25T12:00:00Z')
			}
		});
		const s = screen.getByRole('status');
		expect(s).toHaveTextContent('edge is offline');
		expect(s).toHaveTextContent('Its agent disconnected 3 hours ago.');
		expect(s).toHaveTextContent('Actions on edge are unavailable until it reconnects.');
	});
});

describe('JobProgress', () => {
	it('lists per-item results of a partial failure with the recovery advice', async () => {
		const es = new ScriptedEventSource();
		const watcher = new JobWatcher('j1', {
			eventSource: () => es,
			client: demoJobClient(demoJob('partial'))
		});
		watcher.start();
		render(JobProgress, { props: { watcher, title: 'Prune stopped containers on nas' } });
		es.emit('job', demoJob('running'));
		await waitFor(() =>
			expect(
				screen.getByRole('progressbar', {
					name: 'Prune stopped containers on nas progress'
				})
			).toBeInTheDocument()
		);
		for (const ev of demoJobEvents) es.emit(ev.type, ev);
		await waitFor(() => expect(screen.getByText('Partly failed')).toBeInTheDocument());
		expect(screen.queryByRole('progressbar')).toBeNull();
		expect(screen.getByText('1 of 3 items failed, 2 succeeded')).toBeInTheDocument();
		const items = within(screen.getByRole('list', { name: 'Items' })).getAllByRole('listitem');
		expect(items.map((i) => i.className.split(' ')[0])).toEqual([
			'succeeded',
			'failed',
			'succeeded'
		]);
		expect(items[1]).toHaveTextContent('old-nextcloud');
		expect(items[1]).toHaveTextContent('device or resource busy');
		expect(screen.getByText(/run the job again/)).toBeInTheDocument();
		expect(
			screen
				.getAllByRole('status')
				.some((s) => s.textContent === 'Prune stopped containers on nas: partly failed')
		).toBe(true);
		watcher.stop();
	});
});

describe('JobProgress notices', () => {
	it('announces a job it follows in the notices bell when it finishes', async () => {
		const es = new ScriptedEventSource();
		const notices = new Notices(() => 1);
		const finished = vi.fn();
		render(JobProgress, {
			props: {
				jobId: 'job-9',
				title: 'Prune stopped containers on nas',
				notices,
				onfinish: finished,
				options: { eventSource: () => es, client: demoJobClient(demoJob('partial')) }
			}
		});
		es.emit('job', demoJob('running'));
		for (const ev of demoJobEvents) es.emit(ev.type, ev);
		await waitFor(() => expect(finished).toHaveBeenCalledTimes(1));
		expect(notices.items).toHaveLength(1);
		expect(notices.items[0]).toMatchObject({
			kind: 'job',
			tone: 'warn',
			title: 'Prune stopped containers on nas: partly failed',
			href: `/jobs/${demoJob().id}`
		});
	});
});

describe('SecretReveal', () => {
	it('shows the secret once and continues only after "I stored it"', async () => {
		const user = setup();
		const done = vi.fn();
		render(SecretReveal, {
			props: {
				secret: 'DYRK-AAAA-BBBB',
				label: 'Recovery Key',
				fingerprint: 'rk_0123456789abcdef',
				onconfirm: done
			}
		});
		expect(screen.getByLabelText('Recovery Key')).toHaveTextContent('DYRK-AAAA-BBBB');
		expect(screen.getByText('rk_0123456789abcdef')).toBeInTheDocument();
		const cont = screen.getByRole('button', { name: 'Continue' });
		expect(cont).toBeDisabled();
		await user.click(screen.getByRole('button', { name: 'Copy Recovery Key' }));
		expect(await navigator.clipboard.readText()).toBe('DYRK-AAAA-BBBB');
		await user.click(
			screen.getByRole('checkbox', { name: 'I stored the Recovery Key somewhere safe' })
		);
		await user.click(cont);
		expect(done).toHaveBeenCalledTimes(1);
		expect(screen.queryByText('DYRK-AAAA-BBBB')).toBeNull();
	});
});

describe('StepWizard', () => {
	it('validates each step, moves focus to the new step and finishes', async () => {
		const user = setup();
		let ok = false;
		const finish = vi.fn();
		const step = createRawSnippet((s: () => { label: string }) => ({
			render: () => `<p>Content of ${s().label}</p>`
		}));
		render(StepWizard, {
			props: {
				label: 'Backup setup',
				steps: [
					{ id: 'repo', label: 'Repository' },
					{ id: 'key', label: 'Recovery Key' }
				],
				step,
				onnext: () => {
					if (!ok) throw new Error('choose a destination first');
				},
				onfinish: finish
			}
		});
		const steps = screen.getByRole('list', { name: 'Backup setup steps' });
		expect(within(steps).getAllByRole('listitem')[0]).toHaveAttribute('aria-current', 'step');
		await user.click(screen.getByRole('button', { name: 'Next' }));
		expect(screen.getByRole('alert')).toHaveTextContent('Choose a destination first.');
		ok = true;
		await user.click(screen.getByRole('button', { name: 'Next' }));
		expect(screen.getByRole('heading', { name: 'Recovery Key' })).toHaveFocus();
		expect(screen.getByText('Content of Recovery Key')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Finish' }));
		expect(finish).toHaveBeenCalledTimes(1);
		await user.click(screen.getByRole('button', { name: 'Back' }));
		expect(screen.getByRole('heading', { name: 'Repository' })).toHaveFocus();
	});

	it('offers Cancel on every step and returns to visited steps when they are clickable', async () => {
		const user = setup();
		const cancel = vi.fn();
		const step = createRawSnippet((s: () => { label: string }) => ({
			render: () => `<p>Content of ${s().label}</p>`
		}));
		render(StepWizard, {
			props: {
				label: 'Policy',
				steps: [
					{ id: 'a', label: 'Destination' },
					{ id: 'b', label: 'Schedule' },
					{ id: 'c', label: 'Retention' }
				],
				step,
				oncancel: cancel,
				stepsClickable: true
			}
		});
		// Steps not reached yet are no buttons.
		expect(screen.queryByRole('button', { name: /Schedule/ })).toBeNull();
		await user.click(screen.getByRole('button', { name: 'Next' }));
		await user.click(screen.getByRole('button', { name: 'Next' }));
		expect(screen.getByRole('heading', { name: 'Retention' })).toHaveFocus();
		await user.click(screen.getByRole('button', { name: /Destination/ }));
		expect(screen.getByRole('heading', { name: 'Destination' })).toHaveFocus();
		// A visited later step can be reached again.
		await user.click(screen.getByRole('button', { name: /Retention/ }));
		expect(screen.getByRole('heading', { name: 'Retention' })).toHaveFocus();
		await user.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(cancel).toHaveBeenCalledTimes(1);
	});

	it('says why Next is off while it is', async () => {
		const step = createRawSnippet(() => ({ render: () => '<p>Pick one</p>' }));
		const props = {
			label: 'Setup',
			steps: [
				{ id: 'a', label: 'Destination' },
				{ id: 'b', label: 'Check' }
			],
			step,
			canAdvance: false,
			disabledReason: 'Choose the destination first.'
		};
		const { rerender } = render(StepWizard, { props });
		const next = screen.getByRole('button', { name: 'Next' });
		expect(next).toBeDisabled();
		expect(next).toHaveAttribute('title', 'Choose the destination first.');
		await rerender({ ...props, canAdvance: true });
		await waitFor(() => expect(next).toBeEnabled());
		expect(next).not.toHaveAttribute('title');
	});
});

describe('Toaster', () => {
	it('announces confirmations politely and errors assertively; dismisses', async () => {
		const user = setup();
		const toasts = new Toasts();
		render(Toaster, { props: { toasts } });
		toasts.success('Deployed Silo');
		toasts.error('Silo was not deployed', { body: 'homelab is offline.' });
		await waitFor(() =>
			expect(screen.getByRole('status', { name: 'Notifications' })).toHaveTextContent(
				'Deployed Silo'
			)
		);
		expect(screen.getByRole('alert', { name: 'Errors' })).toHaveTextContent(
			'Silo was not deployed'
		);
		const errorClose = within(screen.getByRole('alert', { name: 'Errors' })).getByRole(
			'button',
			{ name: 'Dismiss' }
		);
		await user.click(errorClose);
		expect(toasts.items.map((t) => t.title)).toEqual(['Deployed Silo']);
		toasts.clear();
	});
});
