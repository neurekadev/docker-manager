import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import DialogHarness from '../../test/DialogHarness.svelte';
import StackedDialogsHarness from '../../test/StackedDialogsHarness.svelte';
import ConfirmDialog from './ConfirmDialog.svelte';
import DestructiveConfirm from './DestructiveConfirm.svelte';
import TooltipLayer from './TooltipLayer.svelte';
import { tooltipAnchor } from './tooltip';
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

	it('stacks a dialog opened over another above it, whatever the mount order', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		render(StackedDialogsHarness);
		const layer = (el: HTMLElement) => Number(el.style.getPropertyValue('--dy-layer'));
		await user.click(screen.getByRole('button', { name: 'Invite user' }));
		const invite = await screen.findByRole('dialog', { name: 'Invite a user' });
		await user.click(screen.getByRole('button', { name: 'Create invitation' }));
		const stepUp = await screen.findByRole('dialog', { name: "Confirm it's you" });
		// The step-up check comes first in the DOM, so it must be the higher layer.
		expect(
			stepUp.compareDocumentPosition(invite) & Node.DOCUMENT_POSITION_FOLLOWING
		).toBeTruthy();
		expect(layer(stepUp)).toBeGreaterThan(layer(invite));
		await waitFor(() => expect(stepUp.contains(document.activeElement)).toBe(true));
		await user.keyboard('{Escape}');
		await waitFor(() =>
			expect(screen.queryByRole('dialog', { name: "Confirm it's you" })).toBeNull()
		);
		expect(screen.getByRole('dialog', { name: 'Invite a user' })).toBeInTheDocument();
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
					message: 'the container belongs to a Docker Manager-managed stack',
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
			'The container belongs to a Docker Manager-managed stack.'
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

	it('shows the name to type as code with a copy button', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const write = vi.spyOn(navigator.clipboard, 'writeText');
		const name = 'dockyard-e2e_e2e-caddy-with-a-very-long-name-that-must-not-wrap';
		render(DestructiveConfirm, {
			props: {
				open: true,
				title: 'Remove container?',
				consequences: ['Removes the container.'],
				confirmText: name,
				confirmLabel: 'Remove container',
				onconfirm: vi.fn()
			}
		});
		const dialog = await screen.findByRole('alertdialog', { name: 'Remove container?' });
		const code = dialog.querySelector('code');
		expect(code).toHaveTextContent(name);
		await user.click(screen.getByRole('button', { name: 'Copy name' }));
		expect(write).toHaveBeenCalledWith(name);
		expect(await navigator.clipboard.readText()).toBe(name);
		expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument();
		// Pasting the copied name enables the action.
		await user.click(screen.getByLabelText(`Type ${name} to confirm`));
		await user.paste(name);
		expect(screen.getByRole('button', { name: 'Remove container' })).toBeEnabled();
	});
});

describe('TooltipLayer', () => {
	it('finds the nearest element with a title, never an SVG one', () => {
		document.body.innerHTML =
			'<span id="a" title="Up to date"><b id="b">x</b></span><svg title="no"><g id="g"></g></svg><i id="c" title=" "></i>';
		expect(tooltipAnchor(document.getElementById('b'))?.id).toBe('a');
		expect(tooltipAnchor(document.getElementById('g'))).toBeNull();
		expect(tooltipAnchor(document.getElementById('c'))).toBeNull();
		expect(tooltipAnchor(null)).toBeNull();
		document.body.innerHTML = '';
	});

	it('shows a title as a themed tooltip on keyboard focus and puts the title back', async () => {
		render(TooltipLayer);
		const button = document.createElement('button');
		button.title = 'Check for updates';
		button.textContent = 'Check';
		document.body.append(button);
		button.matches = ((q: string) => q === ':focus-visible') as typeof button.matches;
		button.focus();
		const tip = await screen.findByRole('tooltip');
		expect(tip).toHaveTextContent('Check for updates');
		expect(button).not.toHaveAttribute('title');
		expect(button).toHaveAccessibleDescription('Check for updates');
		button.blur();
		await waitFor(() => expect(screen.queryByRole('tooltip')).not.toBeInTheDocument());
		expect(button).toHaveAttribute('title', 'Check for updates');
		expect(button).not.toHaveAttribute('aria-describedby');
		button.remove();
	});
});
