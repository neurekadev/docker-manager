// Picks an option of a Select (#22; Bits UI listbox, not a native
// <select>) in component tests: opens the trigger (role combobox) and
// clicks the option with this accessible name.
import { screen } from '@testing-library/svelte';
import type { UserEvent } from '@testing-library/user-event';

export async function choose(
	user: UserEvent,
	trigger: HTMLElement,
	option: string | RegExp
): Promise<void> {
	await user.click(trigger);
	await user.click(await screen.findByRole('option', { name: option }));
}
