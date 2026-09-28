import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import Checkbox from './Checkbox.svelte';
import PasswordField from './PasswordField.svelte';
import RadioGroup from './RadioGroup.svelte';
import Select from './Select.svelte';
import SuggestField from './SuggestField.svelte';
import Switch from './Switch.svelte';
import Tabs from './Tabs.svelte';
import TextField from './TextField.svelte';
import TriState from './TriState.svelte';
import { createRawSnippet } from 'svelte';
import { choose } from '../../test/select';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

describe('fields', () => {
	it('labels controls and links descriptions and errors', () => {
		render(TextField, {
			props: {
				label: 'Stack name',
				description: 'The Compose project name.',
				error: 'Use lowercase letters.',
				value: 'Silo'
			}
		});
		const input = screen.getByLabelText('Stack name');
		expect(input).toHaveAccessibleDescription(
			'The Compose project name. Use lowercase letters.'
		);
		expect(input).toHaveAttribute('aria-invalid', 'true');
	});

	it('reveals and hides a password without losing it', async () => {
		const user = setup();
		render(PasswordField, { props: { label: 'Password', autocomplete: 'new-password' } });
		const input = screen.getByLabelText('Password') as HTMLInputElement;
		expect(input.type).toBe('password');
		expect(input).toHaveAttribute('autocomplete', 'new-password');
		await user.type(input, 'correct horse battery staple');
		await user.click(screen.getByRole('button', { name: 'Show password' }));
		expect(input.type).toBe('text');
		expect(screen.getByRole('button', { name: 'Hide password' })).toHaveAttribute(
			'aria-pressed',
			'true'
		);
		expect(input.value).toBe('correct horse battery staple');
		await user.click(screen.getByRole('button', { name: 'Hide password' }));
		expect(input.type).toBe('password');
	});

	it('select: a themed listbox that shows the choice and reports it', async () => {
		const user = setup();
		const change = vi.fn();
		render(Select, {
			props: {
				label: 'Pull policy',
				value: 'missing',
				onchange: change,
				options: [
					{ value: 'missing', label: 'Pull missing images' },
					{ value: 'always', label: 'Always pull' }
				]
			}
		});
		const trigger = screen.getByRole('combobox', { name: 'Pull policy' });
		expect(trigger).toHaveTextContent('Pull missing images');
		expect(document.querySelector('select')).toBeNull();
		await choose(user, trigger, 'Always pull');
		expect(change).toHaveBeenCalledWith('always');
		expect(trigger).toHaveTextContent('Always pull');
	});

	it('suggest field: any text, with matching suggestions to pick by keyboard or pointer', async () => {
		const user = setup();
		render(SuggestField, {
			props: { label: 'Volume', suggestions: ['silo_data', 'silo_media', 'pihole'] }
		});
		const input = screen.getByRole('combobox', { name: 'Volume' });
		await user.type(input, 'silo');
		expect(screen.getAllByRole('option').map((o) => o.textContent?.trim())).toEqual([
			'silo_data',
			'silo_media'
		]);
		await user.keyboard('{ArrowDown}{ArrowDown}{Enter}');
		expect(input).toHaveValue('silo_media');
		expect(screen.queryByRole('listbox')).toBeNull();
		await user.clear(input);
		await user.type(input, 'brand_new');
		expect(input).toHaveValue('brand_new');
		await user.clear(input);
		await user.type(input, 'pi');
		await user.pointer({ keys: '[MouseLeft>]', target: screen.getByRole('option') });
		expect(input).toHaveValue('pihole');
	});

	it('select: shows the placeholder until something is chosen', () => {
		render(Select, {
			props: {
				label: 'Environment',
				placeholder: 'Choose an environment',
				options: [{ value: 'e1', label: 'prod' }]
			}
		});
		expect(screen.getByRole('combobox', { name: 'Environment' })).toHaveTextContent(
			'Choose an environment'
		);
	});
});

describe('toggles', () => {
	it('switch: role, state and keyboard', async () => {
		const user = setup();
		const change = vi.fn();
		render(Switch, {
			props: { label: 'Follow', description: 'Scroll with new log lines.', onchange: change }
		});
		const sw = screen.getByRole('switch', { name: 'Follow' });
		expect(sw).toHaveAttribute('aria-checked', 'false');
		expect(sw).toHaveAccessibleDescription('Scroll with new log lines.');
		sw.focus();
		await user.keyboard(' ');
		expect(sw).toHaveAttribute('aria-checked', 'true');
		await user.keyboard('{Enter}');
		expect(sw).toHaveAttribute('aria-checked', 'false');
		expect(change.mock.calls).toEqual([[true], [false]]);
	});

	it('checkbox: indeterminate state for mixed selections', async () => {
		const { rerender } = render(Checkbox, {
			props: { label: 'Select all rows', hideLabel: true, indeterminate: true }
		});
		const cb = screen.getByRole('checkbox', { name: 'Select all rows' }) as HTMLInputElement;
		expect(cb.indeterminate).toBe(true);
		await rerender({ indeterminate: false, checked: true });
		expect(cb.indeterminate).toBe(false);
		expect(cb.checked).toBe(true);
	});

	it('radio group: legend names the group, arrows move the choice', async () => {
		const user = setup();
		render(RadioGroup, {
			props: {
				label: 'Restart policy',
				value: 'no',
				options: [
					{ value: 'no', label: 'No' },
					{ value: 'always', label: 'Always' }
				]
			}
		});
		expect(screen.getByRole('group', { name: 'Restart policy' })).toBeInTheDocument();
		screen.getByRole('radio', { name: 'No' }).focus();
		await user.keyboard('{ArrowDown}');
		expect(screen.getByRole('radio', { name: 'Always' })).toBeChecked();
	});

	it('tri-state: Inherit / Allow / Deny with the effective decision explained', async () => {
		const user = setup();
		const change = vi.fn();
		render(TriState, {
			props: {
				label: 'Restart containers on homelab',
				inherited: 'allow',
				inheritedFrom: 'group Operators',
				highRisk: true,
				onchange: change
			}
		});
		const group = screen.getByRole('radiogroup', { name: 'Restart containers on homelab' });
		expect(group).toHaveAccessibleDescription('Inherits Allow from group Operators');
		expect(group).toHaveAttribute('data-effective', 'allow');
		await user.click(screen.getByRole('radio', { name: 'Deny' }));
		expect(change).toHaveBeenCalledWith('deny');
		expect(group).toHaveAttribute('data-effective', 'deny');
		expect(group).toHaveAccessibleDescription(
			'Denied for this user, whatever the group grants'
		);
		expect(screen.getByText('High risk')).toBeInTheDocument();
	});
});

describe('Tabs', () => {
	it('moves between tabs with the arrow keys', async () => {
		const user = setup();
		const panel = createRawSnippet((id: () => string) => ({
			render: () => `<p>Panel ${id()}</p>`
		}));
		render(Tabs, {
			props: {
				label: 'Stack sections',
				items: [
					{ id: 'overview', label: 'Overview' },
					{ id: 'files', label: 'Files' },
					{ id: 'logs', label: 'Logs', count: 2 }
				],
				panel
			}
		});
		expect(screen.getByRole('tablist', { name: 'Stack sections' })).toBeInTheDocument();
		expect(screen.getByText('Panel overview')).toBeInTheDocument();
		screen.getByRole('tab', { name: 'Overview' }).focus();
		await user.keyboard('{ArrowRight}');
		const files = screen.getByRole('tab', { name: 'Files' });
		expect(files).toHaveFocus();
		await user.keyboard('{Enter}');
		expect(files).toHaveAttribute('aria-selected', 'true');
		expect(await screen.findByText('Panel files')).toBeInTheDocument();
	});
});
