import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import Card from './Card.svelte';
import Checkbox from './Checkbox.svelte';
import MultiSelect from './MultiSelect.svelte';
import PasswordField from './PasswordField.svelte';
import RadioGroup from './RadioGroup.svelte';
import Select from './Select.svelte';
import SuggestField from './SuggestField.svelte';
import Switch from './Switch.svelte';
import Tabs from './Tabs.svelte';
import TextField from './TextField.svelte';
import TriState from './TriState.svelte';
import FieldGroup from '../features/common/FieldGroup.svelte';
import { createRawSnippet } from 'svelte';
import { choose } from '../../test/select';
import CronHarness from '../../test/CronHarness.svelte';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

describe('fields', () => {
	it('labels controls and links descriptions and errors', () => {
		render(TextField, {
			props: {
				label: 'Stack Name',
				description: 'The Compose project name.',
				error: 'Use lowercase letters.',
				value: 'Silo'
			}
		});
		const input = screen.getByLabelText('Stack Name');
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
		await user.click(screen.getByRole('button', { name: 'Show Password' }));
		expect(input.type).toBe('text');
		expect(screen.getByRole('button', { name: 'Hide Password' })).toHaveAttribute(
			'aria-pressed',
			'true'
		);
		expect(input.value).toBe('correct horse battery staple');
		await user.click(screen.getByRole('button', { name: 'Hide Password' }));
		expect(input.type).toBe('password');
	});

	it('select: a themed listbox that shows the choice and reports it', async () => {
		const user = setup();
		const change = vi.fn();
		render(Select, {
			props: {
				label: 'Pull Policy',
				value: 'missing',
				onchange: change,
				options: [
					{ value: 'missing', label: 'Pull Missing Images' },
					{ value: 'always', label: 'Always Pull' }
				]
			}
		});
		const trigger = screen.getByRole('combobox', { name: 'Pull Policy' });
		expect(trigger).toHaveTextContent('Pull Missing Images');
		expect(document.querySelector('select')).toBeNull();
		await choose(user, trigger, 'Always Pull');
		expect(change).toHaveBeenCalledWith('always');
		expect(trigger).toHaveTextContent('Always Pull');
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

describe('info tips and optional fields', () => {
	const body = createRawSnippet(() => ({ render: () => '<p>Body</p>' }));
	const tipFor = (text: string) => screen.getByRole('img', { name: text });

	it('field: the (i) follows the label, describes the control and stays out of its name', () => {
		render(TextField, {
			props: {
				label: 'Stack Name',
				description: 'The Compose project name.',
				info: 'Lowercase letters, digits, dashes and underscores.'
			}
		});
		const input = screen.getByRole('textbox', { name: 'Stack Name' });
		expect(tipFor('Lowercase letters, digits, dashes and underscores.')).toHaveAttribute(
			'tabindex',
			'0'
		);
		expect(input).toHaveAccessibleDescription(
			'The Compose project name. Lowercase letters, digits, dashes and underscores.'
		);
		expect(screen.getByLabelText('Stack Name')).toBe(input);
	});

	it('field: optional shows "Optional" beside the label and no description', () => {
		render(TextField, { props: { label: 'Display Name', optional: true } });
		const input = screen.getByLabelText('Display Name');
		expect(input).toHaveAccessibleName('Display Name');
		expect(input).not.toHaveAttribute('aria-describedby');
		expect(input).not.toBeRequired();
		expect(screen.getByText('Optional')).toHaveAttribute('aria-hidden', 'true');
	});

	it('field: no (i) and no "Optional" unless asked for', () => {
		render(TextField, { props: { label: 'Stack Name' } });
		expect(screen.queryByRole('img')).not.toBeInTheDocument();
		expect(screen.queryByText('Optional')).not.toBeInTheDocument();
	});

	it('select: the (i) describes the trigger', () => {
		render(Select, {
			props: {
				label: 'Pull Policy',
				info: 'Always Pull fetches the image on every deploy.',
				options: [{ value: 'always', label: 'Always Pull' }]
			}
		});
		const trigger = screen.getByRole('combobox', { name: 'Pull Policy' });
		expect(tipFor('Always Pull fetches the image on every deploy.')).toBeInTheDocument();
		expect(trigger).toHaveAccessibleDescription(
			'Always Pull fetches the image on every deploy.'
		);
	});

	it('switch: the (i) describes the switch and stays out of its name', () => {
		render(Switch, {
			props: { label: 'Follow', description: 'Live.', info: 'Scrolls with new log lines.' }
		});
		const sw = screen.getByRole('switch', { name: 'Follow' });
		expect(tipFor('Scrolls with new log lines.')).toBeInTheDocument();
		expect(sw).toHaveAccessibleDescription('Live. Scrolls with new log lines.');
	});

	it('checkbox: the (i) sits outside the label and describes the box', () => {
		render(Checkbox, {
			props: { label: 'Include Volumes', info: "Backs up the stack's named volumes too." }
		});
		const box = screen.getByRole('checkbox', { name: 'Include Volumes' });
		const tip = tipFor("Backs up the stack's named volumes too.");
		expect(tip.closest('label')).toBeNull();
		expect(box).toHaveAccessibleDescription("Backs up the stack's named volumes too.");
	});

	it('card: the (i) follows the title and leaves the heading and region names alone', () => {
		render(Card, {
			props: {
				title: 'Services',
				id: 'services',
				info: 'Each service of the stack with its state.',
				children: body
			}
		});
		expect(screen.getByRole('heading', { level: 2, name: 'Services' })).toBeInTheDocument();
		expect(screen.getByRole('region', { name: 'Services' })).toBeInTheDocument();
		expect(tipFor('Each service of the stack with its state.')).toBeInTheDocument();
	});

	it('field group: the (i) describes the group and stays out of its name', () => {
		render(FieldGroup, {
			props: { legend: 'Update Window', info: 'No day checked: every day.', children: body }
		});
		const group = screen.getByRole('group', { name: 'Update Window' });
		expect(tipFor('No day checked: every day.')).toBeInTheDocument();
		expect(group).toHaveAccessibleDescription('No day checked: every day.');
	});

	it('field group: a hint stays visible and describes the group too', () => {
		render(FieldGroup, {
			props: {
				legend: 'Running Containers',
				hint: 'Live backups are crash-consistent.',
				children: body
			}
		});
		const group = screen.getByRole('group', { name: 'Running Containers' });
		expect(screen.getByText('Live backups are crash-consistent.')).toBeInTheDocument();
		expect(group).toHaveAccessibleDescription('Live backups are crash-consistent.');
	});
});

describe('multi select', () => {
	it('toggles options as switches, keeps one with Only and selects all again', async () => {
		const user = setup();
		const change = vi.fn();
		render(MultiSelect, {
			props: {
				label: 'Filter',
				allLabel: 'Everything',
				onchange: change,
				value: ['error', 'warning', 'stdout', 'stderr'],
				groups: [
					{
						label: 'Levels',
						options: [
							{ value: 'error', label: 'Error', count: 3, hue: 'var(--danger)' },
							{ value: 'warning', label: 'Warning', count: 1 }
						]
					},
					{
						label: 'Output',
						options: [
							{ value: 'stdout', label: 'Standard Output' },
							{ value: 'stderr', label: 'Standard Error' }
						]
					}
				]
			}
		});
		const trigger = screen.getByRole('button', { name: 'Filter' });
		expect(trigger).toHaveTextContent('Everything');
		await user.click(trigger);
		const warning = await screen.findByRole('switch', { name: /Warning/ });
		expect(warning).toHaveAttribute('aria-checked', 'true');
		expect(screen.getByRole('switch', { name: /^Error/ })).toHaveTextContent('3');
		await user.click(warning);
		expect(change).toHaveBeenLastCalledWith(['error', 'stdout', 'stderr']);
		expect(warning).toHaveAttribute('aria-checked', 'false');
		expect(trigger).toHaveTextContent('3 of 4');
		// Only keeps one option of its group; the other group stays as it is.
		await user.click(screen.getByRole('button', { name: 'Only Standard Error' }));
		expect(change).toHaveBeenLastCalledWith(['error', 'stderr']);
		await user.click(screen.getByRole('button', { name: 'All Levels' }));
		expect(change).toHaveBeenLastCalledWith(['error', 'warning', 'stderr']);
		await user.click(screen.getByRole('button', { name: 'Select All' }));
		expect(change).toHaveBeenLastCalledWith(['error', 'warning', 'stdout', 'stderr']);
		expect(trigger).toHaveTextContent('Everything');
		expect(screen.queryByRole('button', { name: 'Select All' })).toBeNull();
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
			props: { label: 'Select All Rows', hideLabel: true, indeterminate: true }
		});
		const cb = screen.getByRole('checkbox', { name: 'Select All Rows' }) as HTMLInputElement;
		expect(cb.indeterminate).toBe(true);
		await rerender({ indeterminate: false, checked: true });
		expect(cb.indeterminate).toBe(false);
		expect(cb.checked).toBe(true);
	});

	it('radio group: legend names the group, arrows move the choice', async () => {
		const user = setup();
		render(RadioGroup, {
			props: {
				label: 'Restart Policy',
				value: 'no',
				options: [
					{ value: 'no', label: 'No' },
					{ value: 'always', label: 'Always' }
				]
			}
		});
		expect(screen.getByRole('group', { name: 'Restart Policy' })).toBeInTheDocument();
		screen.getByRole('radio', { name: 'No' }).focus();
		await user.keyboard('{ArrowDown}');
		expect(screen.getByRole('radio', { name: 'Always' })).toBeChecked();
	});

	it('tri-state: Inherit / Allow / Deny with the effective decision explained', async () => {
		const user = setup();
		const change = vi.fn();
		render(TriState, {
			props: {
				label: 'Restart Containers on homelab',
				inherited: 'allow',
				inheritedFrom: 'group Operators',
				highRisk: true,
				onchange: change
			}
		});
		const group = screen.getByRole('radiogroup', { name: 'Restart Containers on homelab' });
		expect(group).toHaveAccessibleDescription('Inherits Allow from group Operators');
		// Inherit hides the effective decision, so it is said on screen.
		expect(screen.getByText('Inherits Allow from group Operators')).not.toHaveClass('sr-only');
		expect(group).toHaveAttribute('data-effective', 'allow');
		await user.click(screen.getByRole('radio', { name: 'Deny' }));
		expect(change).toHaveBeenCalledWith('deny');
		expect(group).toHaveAttribute('data-effective', 'deny');
		expect(group).toHaveAccessibleDescription(
			'Denied for this user, whatever the group grants'
		);
		// An explicit choice shows itself: its explanation is for screen readers only.
		expect(screen.getByText('Denied for this user, whatever the group grants')).toHaveClass(
			'sr-only'
		);
		expect(screen.getByText('High Risk')).toBeInTheDocument();
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
				label: 'Stack Sections',
				items: [
					{ id: 'overview', label: 'Overview' },
					{ id: 'files', label: 'Files' },
					{ id: 'logs', label: 'Logs', count: 2 }
				],
				panel
			}
		});
		expect(screen.getByRole('tablist', { name: 'Stack Sections' })).toBeInTheDocument();
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

describe('CronField presets', () => {
	afterEach(() => vi.unstubAllGlobals());

	function stubPreview() {
		vi.stubGlobal(
			'fetch',
			vi.fn(async (req: Request) => {
				const body = (await req.json()) as { cron: string; timeZone?: string };
				return new Response(
					JSON.stringify({
						cron: body.cron,
						timeZone: body.timeZone ?? 'UTC',
						from: '2026-09-27T00:00:00Z',
						notes: [],
						runs: [
							{ at: '2026-09-28T03:00:00Z', dst: 'none', local: '2026-09-28T03:00' }
						]
					}),
					{ status: 200, headers: { 'Content-Type': 'application/json' } }
				);
			})
		);
	}

	it('edits a daily schedule with fields and writes the expression', async () => {
		stubPreview();
		const user = setup();
		render(CronHarness, { props: { initial: '0 3 * * *' } });
		expect(screen.getByRole('group', { name: 'Check Schedule' })).toBeInTheDocument();
		const repeats = screen.getByRole('combobox', { name: 'Repeats' });
		expect(repeats).toHaveTextContent('Daily');
		expect(screen.getByLabelText('Time')).toHaveValue('03:00');
		// The raw expression is for Custom only.
		expect(screen.queryByLabelText('Cron Expression')).toBeNull();

		await choose(user, repeats, 'Weekly');
		expect(screen.getByTestId('cron')).toHaveTextContent('0 3 * * 1');
		await choose(user, screen.getByRole('combobox', { name: 'Day' }), 'Friday');
		expect(screen.getByTestId('cron')).toHaveTextContent('0 3 * * 5');

		await choose(user, screen.getByRole('combobox', { name: 'Repeats' }), 'Hourly');
		expect(screen.getByTestId('cron')).toHaveTextContent('0 * * * *');
		const minute = screen.getByLabelText('At Minute');
		await user.clear(minute);
		await user.type(minute, '15');
		expect(screen.getByTestId('cron')).toHaveTextContent('15 * * * *');

		await choose(user, screen.getByRole('combobox', { name: 'Repeats' }), 'Custom');
		const raw = screen.getByLabelText('Cron Expression');
		expect(raw).toHaveValue('15 * * * *');
		await user.clear(raw);
		await user.type(raw, '30 7 * * 1-5');
		expect(screen.getByTestId('cron')).toHaveTextContent('30 7 * * 1-5');
		// Still Custom while typing, with the words and the next runs.
		expect(screen.getByRole('combobox', { name: 'Repeats' })).toHaveTextContent('Custom');
		await waitFor(() => expect(screen.getByText('Next Runs')).toBeInTheDocument());
	});

	it('opens expressions the presets cannot edit as Custom', () => {
		stubPreview();
		render(CronHarness, { props: { initial: '*/15 * * * *' } });
		expect(screen.getByRole('combobox', { name: 'Repeats' })).toHaveTextContent('Custom');
		expect(screen.getByLabelText('Cron Expression')).toHaveValue('*/15 * * * *');
	});
});
