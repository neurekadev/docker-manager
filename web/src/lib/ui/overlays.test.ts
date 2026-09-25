import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import DialogHarness from '../../test/DialogHarness.svelte';
import ConfirmDialog from './ConfirmDialog.svelte';
import DestructiveConfirm from './DestructiveConfirm.svelte';
import { ApiRequestError } from '$lib/api/client';

describe('Dialog', () => {
	it('traps focus, closes on Escape and returns focus to the opener', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		render(DialogHarness);
		const opener = screen.getByRole('button', { name: 'Edit details' });
		await user.click(opener);
		const dialog = await screen.findByRole('dialog', { name: 'Edit details' });
		expect(dialog).toHaveAccessibleDescription('Display metadata');
		await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
		// Tab never leaves the dialog.
		for (let i = 0; i < 6; i++) {
			await user.tab();
			expect(dialog.contains(document.activeElement)).toBe(true);
		}
		await user.keyboard('{Escape}');
		await waitFor(() => expect(screen.getByTestId('state')).toHaveTextContent('closed'));
		await waitFor(() => expect(document.activeElement).toBe(opener));
	});

	it('ignores Escape when not dismissible (busy)', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		render(DialogHarness, { props: { dismissible: false } });
		await user.click(screen.getByRole('button', { name: 'Edit details' }));
		await screen.findByRole('dialog');
		await user.keyboard('{Escape}');
		expect(screen.getByTestId('state')).toHaveTextContent('open');
		expect(screen.queryByRole('button', { name: 'Close' })).toBeNull();
	});
});

describe('ConfirmDialog', () => {
	it('shows a failure inline and stays open; closes after success', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		let fail = true;
		const onconfirm = vi.fn(async () => {
			if (fail) {
				throw new ApiRequestError('x', 409, {
					code: 'stack_managed',
					message: 'the container belongs to a DockYard-managed stack',
					requestId: 'r1',
					retryable: false,
					details: []
				});
			}
		});
		render(ConfirmDialog, {
			props: {
				open: true,
				title: 'Restart Silo?',
				message: 'Restarts 5 containers of Silo.',
				confirmLabel: 'Restart',
				onconfirm
			}
		});
		const dialog = await screen.findByRole('alertdialog', { name: 'Restart Silo?' });
		expect(dialog).toHaveTextContent('Restarts 5 containers of Silo.');
		await user.click(screen.getByRole('button', { name: 'Restart' }));
		expect(await screen.findByRole('alert')).toHaveTextContent(
			'The container belongs to a DockYard-managed stack.'
		);
		fail = false;
		await user.click(screen.getByRole('button', { name: 'Restart' }));
		await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
		expect(onconfirm).toHaveBeenCalledTimes(2);
	});
});

describe('DestructiveConfirm', () => {
	it('lists consequences and affected resources and needs the typed name', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const onconfirm = vi.fn();
		render(DestructiveConfirm, {
			props: {
				open: true,
				title: 'Delete Silo?',
				consequences: ['Removes 3 containers. Volumes and files are kept.'],
				affected: [{ label: 'silo-web-1', detail: 'running' }, { label: 'silo-db-1' }],
				confirmText: 'silo',
				confirmLabel: 'Delete stack',
				onconfirm
			}
		});
		const dialog = await screen.findByRole('alertdialog', { name: 'Delete Silo?' });
		expect(dialog).toHaveTextContent('Removes 3 containers. Volumes and files are kept.');
		expect(dialog).toHaveTextContent('Affected (2)');
		const button = screen.getByRole('button', { name: 'Delete stack' });
		expect(button).toBeDisabled();
		const input = screen.getByLabelText('Type silo to confirm');
		await user.type(input, 'Silo');
		expect(button).toBeDisabled();
		await user.clear(input);
		await user.type(input, 'silo');
		expect(button).toBeEnabled();
		await user.click(button);
		expect(onconfirm).toHaveBeenCalledTimes(1);
	});
});
