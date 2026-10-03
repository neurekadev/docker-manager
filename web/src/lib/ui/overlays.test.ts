import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import DialogHarness from '../../test/DialogHarness.svelte';
import StackedDialogsHarness from '../../test/StackedDialogsHarness.svelte';
import ConfirmDialog from './ConfirmDialog.svelte';
import DestructiveConfirm from './DestructiveConfirm.svelte';
import TooltipLayer from './TooltipLayer.svelte';
import { infoAnchor, tooltipAnchor } from './tooltip';
import { ApiRequestError } from '$lib/api/client';

describe('Dialog', () => {
	it('traps focus, closes on Escape and returns focus to the opener', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		render(DialogHarness);
		const opener = screen.getByRole('button', { name: 'Edit Details' });
		await user.click(opener);
		const dialog = await screen.findByRole('dialog', { name: 'Edit Details' });
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
		await user.click(screen.getByRole('button', { name: 'Invite User' }));
		const invite = await screen.findByRole('dialog', { name: 'Invite a User' });
		await user.click(screen.getByRole('button', { name: 'Create Invitation' }));
		const stepUp = await screen.findByRole('dialog', { name: "Confirm It's You" });
		// The step-up check comes first in the DOM, so it must be the higher layer.
		expect(
			stepUp.compareDocumentPosition(invite) & Node.DOCUMENT_POSITION_FOLLOWING
		).toBeTruthy();
		expect(layer(stepUp)).toBeGreaterThan(layer(invite));
		await waitFor(() => expect(stepUp.contains(document.activeElement)).toBe(true));
		await user.keyboard('{Escape}');
		await waitFor(() =>
			expect(screen.queryByRole('dialog', { name: "Confirm It's You" })).toBeNull()
		);
		expect(screen.getByRole('dialog', { name: 'Invite a User' })).toBeInTheDocument();
	});

	it('ignores Escape when not dismissible (busy)', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		render(DialogHarness, { props: { dismissible: false } });
		await user.click(screen.getByRole('button', { name: 'Edit Details' }));
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
				confirmLabel: 'Delete Stack',
				onconfirm
			}
		});
		const dialog = await screen.findByRole('alertdialog', { name: 'Delete Silo?' });
		expect(dialog).toHaveTextContent('Removes 3 containers. Volumes and files are kept.');
		expect(dialog).toHaveTextContent('Affected (2)');
		const button = screen.getByRole('button', { name: 'Delete Stack' });
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
				confirmLabel: 'Remove Container',
				onconfirm: vi.fn()
			}
		});
		const dialog = await screen.findByRole('alertdialog', { name: 'Remove container?' });
		const code = dialog.querySelector('code');
		expect(code).toHaveTextContent(name);
		await user.click(screen.getByRole('button', { name: 'Copy Name' }));
		expect(write).toHaveBeenCalledWith(name);
		expect(await navigator.clipboard.readText()).toBe(name);
		expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument();
		// Pasting the copied name enables the action.
		await user.click(screen.getByLabelText(`Type ${name} to confirm`));
		await user.paste(name);
		expect(screen.getByRole('button', { name: 'Remove Container' })).toBeEnabled();
	});
});

describe('TooltipLayer', () => {
	it('finds the nearest element with a title, never an SVG one', () => {
		document.body.innerHTML =
			'<span id="a" title="Up to Date"><b id="b">x</b></span><svg title="no"><g id="g"></g></svg><i id="c" title=" "></i>';
		expect(tooltipAnchor(document.getElementById('b'))?.id).toBe('a');
		expect(tooltipAnchor(document.getElementById('g'))).toBeNull();
		expect(tooltipAnchor(document.getElementById('c'))).toBeNull();
		expect(tooltipAnchor(null)).toBeNull();
		document.body.innerHTML = '';
	});

	it('shows a title as a themed tooltip on keyboard focus and puts the title back', async () => {
		render(TooltipLayer);
		const button = document.createElement('button');
		button.title = 'Check for Updates';
		button.textContent = 'Check';
		document.body.append(button);
		button.matches = ((q: string) => q === ':focus-visible') as typeof button.matches;
		button.focus();
		const tip = await screen.findByRole('tooltip');
		expect(tip).toHaveTextContent('Check for Updates');
		expect(button).not.toHaveAttribute('title');
		expect(button).toHaveAccessibleDescription('Check for Updates');
		button.blur();
		await waitFor(() => expect(screen.queryByRole('tooltip')).not.toBeInTheDocument());
		expect(button).toHaveAttribute('title', 'Check for Updates');
		expect(button).not.toHaveAttribute('aria-describedby');
		button.remove();
	});

	it('finds info tips only where data-dy-info marks the titled element', () => {
		document.body.innerHTML =
			'<span id="i" title="Why" data-dy-info><b id="ib">i</b></span><span id="t" title="Plain"></span>';
		expect(infoAnchor(document.getElementById('ib'))?.id).toBe('i');
		expect(infoAnchor(document.getElementById('t'))).toBeNull();
		expect(infoAnchor(null)).toBeNull();
		document.body.innerHTML = '';
	});

	// A tap as touch screens send it; resolves to false when its click was
	// cancelled.
	const tap = async (el: Element, { out = true } = {}) => {
		await fireEvent.pointerDown(el, { pointerType: 'touch' });
		if (out) await fireEvent.pointerOut(el, { pointerType: 'touch' });
		await fireEvent.pointerUp(el, { pointerType: 'touch' });
		return fireEvent.click(el);
	};

	it('toggles an info tip on tap, without activating its summary, and hides it on a tap elsewhere', async () => {
		render(TooltipLayer);
		const details = document.createElement('details');
		details.innerHTML =
			'<summary>Labels<span id="tap-tip" role="img" aria-label="From Compose" title="From Compose" data-dy-info>i</span></summary>';
		const other = document.createElement('p');
		document.body.append(details, other);
		try {
			const tip = document.getElementById('tap-tip')!;

			expect(await tap(tip)).toBe(false);
			expect(details.open).toBe(false);
			expect(await screen.findByRole('tooltip')).toHaveTextContent('From Compose');
			// The accessible name already says it: no second description.
			expect(tip).not.toHaveAttribute('aria-describedby');

			await tap(tip);
			await waitFor(() => expect(screen.queryByRole('tooltip')).not.toBeInTheDocument());
			expect(tip).toHaveAttribute('title', 'From Compose');

			await tap(tip);
			await screen.findByRole('tooltip');
			await tap(other);
			await waitFor(() => expect(screen.queryByRole('tooltip')).not.toBeInTheDocument());
		} finally {
			details.remove();
			other.remove();
		}
	});

	it('keeps a tapped info tip open when focus leaves a container around it', async () => {
		render(TooltipLayer);
		const region = document.createElement('div');
		region.tabIndex = -1;
		region.innerHTML =
			'<span id="focus-tip" role="img" tabindex="0" aria-label="Why" title="Why" data-dy-info>i</span>';
		document.body.append(region);
		try {
			region.focus();
			const tip = document.getElementById('focus-tip')!;
			await fireEvent.pointerDown(tip, { pointerType: 'touch' });
			await fireEvent.pointerUp(tip, { pointerType: 'touch' });
			tip.focus();
			await fireEvent.click(tip);
			expect(await screen.findByRole('tooltip')).toHaveTextContent('Why');
			tip.blur();
			await waitFor(() => expect(screen.queryByRole('tooltip')).not.toBeInTheDocument());
		} finally {
			region.remove();
		}
	});

	it('shows nothing when a plain title is tapped', async () => {
		render(TooltipLayer);
		const span = document.createElement('span');
		span.title = 'Up to date';
		document.body.append(span);
		try {
			expect(await tap(span, { out: false })).toBe(true);
			expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
		} finally {
			span.remove();
		}
	});
});
