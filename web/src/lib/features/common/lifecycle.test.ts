import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import LifecycleButton from './LifecycleButton.svelte';
import type { LifecycleActions } from './lifecycle';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

function actions(over: LifecycleActions = {}) {
	const start = vi.fn();
	const restart = vi.fn();
	const stop = vi.fn();
	return {
		spies: { start, restart, stop },
		actions: {
			start: { run: start },
			restart: { run: restart },
			stop: { run: stop },
			...over
		} as LifecycleActions
	};
}

async function menuItems(user: ReturnType<typeof setup>) {
	await user.click(screen.getByRole('button', { name: 'More start and stop options' }));
	const menu = await screen.findByRole('menu');
	return within(menu).getAllByRole('menuitem');
}

describe('LifecycleButton', () => {
	it('stops by default while it runs, in the danger tone with the stop icon', async () => {
		const user = setup();
		const { spies, actions: a } = actions({
			start: { run: vi.fn(), disabled: true }
		});
		render(LifecycleButton, { props: { running: true, actions: a } });
		const main = screen.getByRole('button', { name: 'Stop' });
		expect(main).toHaveClass('danger-soft');
		expect(main.querySelector('svg')).toHaveClass('lucide-square');
		await user.click(main);
		expect(spies.stop).toHaveBeenCalledTimes(1);
		expect(spies.start).not.toHaveBeenCalled();
	});

	it('starts by default while nothing runs, in the ok tone with the play icon', async () => {
		const user = setup();
		const { spies, actions: a } = actions({
			restart: { run: vi.fn(), disabled: true },
			stop: { run: vi.fn(), disabled: true }
		});
		render(LifecycleButton, { props: { running: false, actions: a } });
		const main = screen.getByRole('button', { name: 'Start' });
		expect(main).toHaveClass('ok-soft');
		expect(main.querySelector('svg')).toHaveClass('lucide-play');
		expect(screen.queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument();
		await user.click(main);
		expect(spies.start).toHaveBeenCalledTimes(1);
	});

	it('lists Start, Restart and Stop in its menu, the ones that do not apply turned off', async () => {
		const user = setup();
		const restart = vi.fn();
		render(LifecycleButton, {
			props: {
				running: true,
				actions: {
					start: { run: vi.fn(), disabled: true },
					restart: { run: restart },
					stop: { run: vi.fn() }
				}
			}
		});
		const items = await menuItems(user);
		expect(items.map((i) => i.textContent?.trim())).toEqual(['Start', 'Restart', 'Stop']);
		expect(items[0]).toHaveAttribute('aria-disabled', 'true');
		expect(items[1]).not.toHaveAttribute('aria-disabled', 'true');
		await user.click(items[1]);
		expect(restart).toHaveBeenCalledTimes(1);
	});

	it('hides the actions the caller does not hold, and is a plain button with one', async () => {
		const stop = vi.fn();
		render(LifecycleButton, { props: { running: true, actions: { stop: { run: stop } } } });
		expect(screen.getByRole('button', { name: 'Stop' })).toBeEnabled();
		expect(
			screen.queryByRole('button', { name: 'More start and stop options' })
		).not.toBeInTheDocument();
	});

	it('shows nothing when the state offers no held action', () => {
		render(LifecycleButton, {
			props: {
				running: false,
				actions: { restart: { run: vi.fn() }, stop: { run: vi.fn() } }
			}
		});
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('keeps a protected Stop and Restart visible but off, with the reason', async () => {
		const user = setup();
		const reason = 'Docker Manager cannot stop its own stack.';
		const { spies, actions: a } = actions({
			start: { run: vi.fn(), disabled: true },
			restart: { run: vi.fn(), disabled: true, reason },
			stop: { run: vi.fn(), disabled: true, reason }
		});
		render(LifecycleButton, { props: { running: true, actions: a } });
		const main = screen.getByRole('button', { name: 'Stop' });
		expect(main).toBeDisabled();
		expect(main).toHaveAttribute('title', reason);
		// The menu stays open to read why.
		const items = await menuItems(user);
		expect(items[1]).toHaveAttribute('aria-disabled', 'true');
		expect(items[1]).toHaveAccessibleDescription(reason);
		expect(items[2]).toHaveAccessibleDescription(reason);
		expect(spies.stop).not.toHaveBeenCalled();
	});

	it('turns everything off while offline or busy with another operation', () => {
		const { actions: a } = actions();
		render(LifecycleButton, {
			props: { running: true, actions: a, disabled: true, reason: 'Renaming Silo…' }
		});
		const main = screen.getByRole('button', { name: 'Stop' });
		expect(main).toBeDisabled();
		expect(main).toHaveAttribute('title', 'Renaming Silo…');
		expect(screen.getByRole('button', { name: 'More start and stop options' })).toBeDisabled();
	});

	it('shows the running action with a spinner on the main part', () => {
		const { actions: a } = actions();
		render(LifecycleButton, { props: { running: true, actions: a, busy: 'restart' } });
		const main = screen.getByRole('button', { name: 'Restart' });
		expect(main).toHaveAttribute('aria-busy', 'true');
		expect(main).toBeDisabled();
		expect(screen.getByRole('button', { name: 'More start and stop options' })).toBeDisabled();
	});
});
