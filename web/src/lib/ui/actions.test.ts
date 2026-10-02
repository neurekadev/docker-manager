import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import Box from '@lucide/svelte/icons/box';
import MenuHarness from '../../test/MenuHarness.svelte';
import Button from './Button.svelte';
import IconButton from './IconButton.svelte';
import StatusBadge from './StatusBadge.svelte';
import { createRawSnippet } from 'svelte';

const text = (s: string) => createRawSnippet(() => ({ render: () => `<span>${s}</span>` }));

describe('Button', () => {
	it('names the action, and loading keeps the label while busy', async () => {
		const click = vi.fn();
		const { rerender } = render(Button, {
			props: { variant: 'primary', onclick: click, children: text('Deploy') }
		});
		const b = screen.getByRole('button', { name: 'Deploy' });
		await userEvent.click(b);
		expect(click).toHaveBeenCalledTimes(1);
		await rerender({ loading: true });
		expect(b).toHaveAttribute('aria-busy', 'true');
		expect(b).toBeDisabled();
		expect(screen.getByTestId('spinner')).toBeInTheDocument();
	});

	it('renders links styled as buttons', () => {
		render(Button, { props: { href: '/stacks', children: text('Open Stacks') } });
		expect(screen.getByRole('link', { name: 'Open Stacks' })).toHaveAttribute(
			'href',
			'/stacks'
		);
	});
});

describe('IconButton', () => {
	it('uses its required label as the accessible name and shows it as a tooltip', async () => {
		render(IconButton, { props: { label: 'Open a terminal in silo-api', icon: Box } });
		const b = screen.getByRole('button', { name: 'Open a terminal in silo-api' });
		await userEvent.hover(b);
		await waitFor(
			() =>
				expect(screen.getAllByText('Open a terminal in silo-api').length).toBeGreaterThan(
					0
				),
			{ timeout: 2000 }
		);
	});

	it('reports a toggle state and a badge count', () => {
		render(IconButton, { props: { label: 'Follow', icon: Box, pressed: true, badge: 3 } });
		expect(screen.getByRole('button', { name: 'Follow' })).toHaveAttribute(
			'aria-pressed',
			'true'
		);
		expect(screen.getByText('3')).toHaveAttribute('aria-hidden', 'true');
	});
});

describe('Menu and SplitButton', () => {
	it('opens from the keyboard, skips disabled items and runs the chosen one', async () => {
		const pick = vi.fn();
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		render(MenuHarness, { props: { onpick: pick } });
		const trigger = screen.getByRole('button', { name: 'More Actions for silo-web' });
		trigger.focus();
		await user.keyboard('{Enter}');
		const menu = await screen.findByRole('menu');
		expect(menu).toHaveAttribute('aria-label', 'Actions for silo-web');
		const items = screen.getAllByRole('menuitem');
		expect(items.map((i) => i.textContent?.trim())).toEqual([
			'Restart',
			'Disabled Action',
			'Remove'
		]);
		expect(items[1]).toHaveAttribute('aria-disabled', 'true');
		await user.click(screen.getByRole('menuitem', { name: 'Remove' }));
		expect(pick).toHaveBeenCalledWith('remove');
		await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
	});

	it('runs the default action or a variant from the chevron', async () => {
		const pick = vi.fn();
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		render(MenuHarness, { props: { onpick: pick } });
		await user.click(screen.getByRole('button', { name: 'Deploy' }));
		expect(pick).toHaveBeenLastCalledWith('deploy');
		await user.click(screen.getByRole('button', { name: 'More Deploy Options' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Build and Deploy' }));
		expect(pick).toHaveBeenLastCalledWith('build');
		expect(screen.getByRole('group', { name: 'Deploy' })).toBeInTheDocument();
	});
});

describe('StatusBadge', () => {
	it('pairs the dot with text, never colour alone', () => {
		const { container } = render(StatusBadge, { props: { status: 'offline' } });
		expect(screen.getByText('Offline')).toBeInTheDocument();
		expect(container.querySelector('.dot')).toHaveAttribute('aria-hidden', 'true');
	});
});
