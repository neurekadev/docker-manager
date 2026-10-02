// Component tests of the permission editor (#17): tree selection,
// plain-language actions in collapsible sections, risk marks, collapsed
// rare actions, section-wide changes, presets, inheritance explanation
// and the save bar.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import { choose } from '../../../test/select';
import ActionMatrix from './ActionMatrix.svelte';
import PermissionEditor from './PermissionEditor.svelte';
import RulesSaveBar from './RulesSaveBar.svelte';
import type { Catalog, Rule } from './permissions';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });
const scopes = (instance: boolean, environment: boolean, resourceTypes: string[] = []) => ({
	instance,
	environment,
	resourceTypes
});
const catalog: Catalog = {
	version: 1,
	resourceTypes: [
		{
			key: 'container',
			label: 'Containers',
			environmentBound: true,
			namedPerEnvironment: true,
			parents: [],
			scopable: true
		},
		{
			key: 'stack',
			label: 'Stacks',
			environmentBound: true,
			namedPerEnvironment: false,
			parents: [],
			scopable: true
		}
	],
	capabilities: [
		{
			key: 'container.restart',
			label: 'Restart',
			description: 'Restart a container.',
			resourceType: 'container',
			risk: 'normal',
			advanced: false,
			ownerOnly: false,
			since: 1,
			scopes: scopes(true, true, ['container'])
		},
		{
			key: 'container.start',
			label: 'Start',
			description: 'Start it.',
			resourceType: 'container',
			risk: 'normal',
			advanced: false,
			ownerOnly: false,
			since: 1,
			scopes: scopes(true, true, ['container'])
		},
		{
			key: 'container.exec',
			label: 'Open terminal',
			description: 'Run commands.',
			resourceType: 'container',
			risk: 'high',
			advanced: false,
			ownerOnly: false,
			since: 1,
			scopes: scopes(true, true, ['container'])
		},
		{
			key: 'container.pause',
			label: 'Pause',
			description: 'Pause it.',
			resourceType: 'container',
			risk: 'normal',
			advanced: true,
			ownerOnly: false,
			since: 1,
			scopes: scopes(true, true, ['container'])
		},
		{
			key: 'stack.deploy',
			label: 'Deploy',
			description: 'Deploy.',
			resourceType: 'stack',
			risk: 'normal',
			advanced: false,
			ownerOnly: false,
			since: 1,
			scopes: scopes(true, true, ['stack'])
		},
		{
			key: 'stack.create',
			label: 'Create stacks',
			description: 'Create.',
			resourceType: 'stack',
			risk: 'normal',
			advanced: false,
			ownerOnly: false,
			since: 1,
			scopes: scopes(true, true)
		}
	]
};
const all = {
	key: 'instance',
	label: 'All Resources',
	scope: { kind: 'instance' as const },
	type: 'instance'
};

function json(body: unknown) {
	return new Response(JSON.stringify(body), {
		status: 200,
		headers: { 'Content-Type': 'application/json' }
	});
}

afterEach(() => vi.unstubAllGlobals());

describe('ActionMatrix', () => {
	it('shows plain-language actions in collapsed sections, marks high risk once and folds rare ones', async () => {
		const user = setup();
		render(ActionMatrix, {
			props: { catalog, node: all, mode: 'group', rules: [], onchange: vi.fn() }
		});
		const region = screen.getByRole('region', { name: 'Actions for All Resources' });
		expect(within(region).getByRole('heading', { name: /^Containers/ })).toBeInTheDocument();
		const containers = within(region).getByRole('button', { name: /^Containers/ });
		expect(containers).toHaveAttribute('aria-expanded', 'false');
		expect(containers).toHaveTextContent('4 actions');
		expect(
			within(region).queryByRole('radiogroup', { name: 'Restart for All Resources' })
		).toBeNull();
		await user.click(containers);
		expect(containers).toHaveAttribute('aria-expanded', 'true');
		expect(
			within(region).getByRole('radiogroup', { name: 'Open terminal for All Resources' })
		).not.toHaveTextContent('High Risk');
		expect(within(region).getAllByText('High Risk')).toHaveLength(1);
		expect(within(region).getByText('1 less common action')).toBeInTheDocument();
		expect(
			within(region).queryByRole('radiogroup', { name: 'Pause for All Resources' })
		).not.toBeVisible();
		expect(within(region).getByText(/including ones added later/)).toBeInTheDocument();
	});

	it('opens the sections that have rules at this scope', () => {
		render(ActionMatrix, {
			props: {
				catalog,
				node: all,
				mode: 'group',
				rules: [
					{
						capability: 'container.restart',
						scope: { kind: 'instance' },
						effect: 'allow'
					}
				],
				onchange: vi.fn()
			}
		});
		const containers = screen.getByRole('button', { name: /^Containers/ });
		expect(containers).toHaveAttribute('aria-expanded', 'true');
		expect(containers).toHaveTextContent('1 allowed of 4 actions');
		expect(screen.getByRole('button', { name: /^Stacks/ })).toHaveAttribute(
			'aria-expanded',
			'false'
		);
	});

	it('sets a group rule with No rule / Allow / Deny', async () => {
		const user = setup();
		const onchange = vi.fn();
		render(ActionMatrix, { props: { catalog, node: all, mode: 'group', rules: [], onchange } });
		await user.click(screen.getByRole('button', { name: /^Containers/ }));
		const restart = screen.getByRole('radiogroup', { name: 'Restart for All Resources' });
		expect(restart).toHaveTextContent('No Rule');
		await user.click(within(restart).getByText('Allow'));
		expect(onchange).toHaveBeenLastCalledWith([
			{ capability: 'container.restart', scope: { kind: 'instance' }, effect: 'allow' }
		]);
	});

	it('allows or clears a whole section at once, without a confirmation', async () => {
		const user = setup();
		const onchange = vi.fn();
		const instance = { kind: 'instance' as const };
		const { rerender } = render(ActionMatrix, {
			props: {
				catalog,
				node: all,
				mode: 'group',
				rules: [
					{ capability: 'container.start', scope: instance, effect: 'deny' },
					{ capability: 'stack.deploy', scope: instance, effect: 'allow' }
				],
				onchange
			}
		});
		await user.click(screen.getByRole('button', { name: 'Allow All Containers' }));
		expect(screen.queryByRole('alertdialog')).toBeNull();
		const allowed = [
			{ capability: 'stack.deploy', scope: instance, effect: 'allow' },
			{ capability: 'container.restart', scope: instance, effect: 'allow' },
			{ capability: 'container.start', scope: instance, effect: 'allow' },
			{ capability: 'container.exec', scope: instance, effect: 'allow' },
			{ capability: 'container.pause', scope: instance, effect: 'allow' }
		];
		expect(onchange).toHaveBeenLastCalledWith(allowed);
		await rerender({ catalog, node: all, mode: 'group', rules: allowed as Rule[], onchange });
		await user.click(screen.getByRole('button', { name: 'Clear Containers' }));
		expect(onchange).toHaveBeenLastCalledWith([
			{ capability: 'stack.deploy', scope: instance, effect: 'allow' }
		]);
	});

	it('starts a group from a preset at the chosen scope only', async () => {
		const user = setup();
		const onchange = vi.fn();
		const stack = {
			key: 'res:stack::s1',
			label: 'Silo',
			scope: { kind: 'resource' as const, resourceType: 'stack', resourceId: 's1' },
			type: 'stack',
			environmentId: 'e1'
		};
		const elsewhere: Rule = {
			capability: 'container.exec',
			scope: { kind: 'instance' },
			effect: 'allow'
		};
		render(ActionMatrix, {
			props: { catalog, node: stack, mode: 'group', rules: [elsewhere], onchange }
		});
		await choose(user, screen.getByRole('combobox', { name: 'Start From' }), 'Operator');
		expect(onchange).toHaveBeenLastCalledWith([
			elsewhere,
			{ capability: 'stack.deploy', scope: stack.scope, effect: 'allow' }
		]);
	});

	it('names the preset the rules match', () => {
		const instance = { kind: 'instance' as const };
		render(ActionMatrix, {
			props: {
				catalog,
				node: all,
				mode: 'group',
				rules: [
					'container.restart',
					'container.start',
					'container.exec',
					'container.pause',
					'stack.deploy',
					'stack.create'
				].map((capability) => ({ capability, scope: instance, effect: 'allow' as const })),
				onchange: vi.fn()
			}
		});
		expect(screen.getByRole('combobox', { name: 'Start From' })).toHaveTextContent('Admin');
	});

	it('explains what a user inherits from the group, and overrides it', async () => {
		const user = setup();
		const onchange = vi.fn();
		const groupRules: Rule[] = [
			{ capability: 'container.restart', scope: { kind: 'instance' }, effect: 'allow' }
		];
		render(ActionMatrix, {
			props: {
				catalog,
				node: all,
				mode: 'user',
				rules: [],
				groupRules,
				groupName: 'Operators',
				onchange
			}
		});
		expect(screen.queryByRole('combobox', { name: 'Start From' })).toBeNull();
		await user.click(screen.getByRole('button', { name: /^Containers/ }));
		const restart = screen.getByRole('radiogroup', { name: 'Restart for All Resources' });
		expect(restart).toHaveAccessibleDescription(
			/Inherits Allow from group Operators, rule for everywhere/
		);
		expect(
			screen.getByRole('radiogroup', { name: 'Start for All Resources' })
		).toHaveAccessibleDescription(/Inherits Deny from group Operators \(no rule\)/);
		await user.click(within(restart).getByText('Deny'));
		expect(onchange).toHaveBeenLastCalledWith([
			{ capability: 'container.restart', scope: { kind: 'instance' }, effect: 'deny' }
		]);
	});

	it('shows the environment rule a stack inherits though its scope names no environment', async () => {
		const user = setup();
		render(ActionMatrix, {
			props: {
				catalog,
				node: {
					key: 'res:stack::s1',
					label: 'Silo',
					scope: { kind: 'resource', resourceType: 'stack', resourceId: 's1' },
					type: 'stack',
					environmentId: 'e1'
				},
				mode: 'user',
				rules: [],
				groupRules: [
					{
						capability: 'stack.deploy',
						scope: { kind: 'environment', environmentId: 'e1' },
						effect: 'allow'
					}
				],
				groupName: 'Operators',
				environmentName: () => 'homelab',
				onchange: vi.fn()
			}
		});
		expect(screen.getByRole('heading', { name: 'Silo on homelab' })).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: /^Stacks/ }));
		expect(
			screen.getByRole('radiogroup', { name: 'Deploy for Silo' })
		).toHaveAccessibleDescription(
			/Inherits Allow from group Operators, rule for this environment/
		);
	});

	it('offers a token only the actions its user holds (#31)', async () => {
		const user = setup();
		render(ActionMatrix, {
			props: {
				catalog,
				node: all,
				mode: 'token',
				rules: [],
				held: new Set(['stack.deploy']),
				onchange: vi.fn()
			}
		});
		expect(screen.queryByRole('button', { name: /^Containers/ })).toBeNull();
		expect(screen.getByRole('button', { name: 'Grant All Stacks' })).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: /^Stacks/ }));
		expect(screen.getByRole('checkbox', { name: 'Grant Deploy' })).toBeInTheDocument();
		expect(screen.queryByRole('checkbox', { name: 'Grant Restart' })).toBeNull();
	});
});

describe('PermissionEditor', () => {
	it('selects a scope in the tree and shows its actions', async () => {
		const user = setup();
		vi.stubGlobal(
			'fetch',
			vi.fn(async (req: Request) => {
				const path = new URL(req.url).pathname;
				if (path === '/api/v1/environments')
					return json({
						items: [
							{
								id: 'e1',
								name: 'homelab',
								online: true,
								status: 'active',
								view: 'full',
								actions: []
							}
						]
					});
				return json({ items: [] });
			})
		);
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(QueryHarness<ComponentProps<typeof PermissionEditor>>, {
			props: {
				client,
				component: PermissionEditor,
				props: {
					catalog,
					mode: 'group',
					rules: [
						{
							capability: 'stack.deploy',
							scope: { kind: 'environment', environmentId: 'e1' },
							effect: 'allow'
						}
					],
					onchange: vi.fn()
				}
			}
		});
		const tree = screen.getByRole('navigation', { name: 'Resources' });
		const env = await within(tree).findByRole('button', { name: /^homelab/ });
		expect(env).toHaveTextContent('1');
		await user.click(env);
		expect(env).toHaveAttribute('aria-current', 'true');
		const region = screen.getByRole('region', { name: 'Actions for homelab' });
		expect(
			within(region).getByRole('radiogroup', { name: 'Deploy for homelab' })
		).toHaveTextContent('Allowed for members');
		await user.click(within(tree).getByRole('button', { name: 'Expand homelab' }));
		expect(within(tree).getByRole('button', { name: 'Stacks' })).toHaveAttribute(
			'aria-expanded',
			'false'
		);
	});
});

describe('RulesSaveBar', () => {
	it('lists every change before saving', async () => {
		const user = setup();
		const onsave = vi.fn(async () => {});
		render(RulesSaveBar, {
			props: {
				before: [],
				after: [
					{
						capability: 'container.restart',
						scope: { kind: 'instance' },
						effect: 'allow'
					}
				],
				catalog,
				subject: 'Operators',
				mode: 'group',
				ondiscard: vi.fn(),
				onsave
			}
		});
		expect(
			screen.getByRole('region', { name: 'Unsaved Permission Changes' })
		).toHaveTextContent('1 unsaved change');
		await user.click(screen.getByRole('button', { name: 'Save Permissions' }));
		const dialog = await screen.findByRole('alertdialog', {
			name: 'Save the permissions of Operators?'
		});
		expect(dialog).toHaveTextContent('Restart (Containers) on everything: No Rule → Allow');
		await user.click(within(dialog).getByRole('button', { name: 'Save Permissions' }));
		expect(onsave).toHaveBeenCalled();
	});
});
