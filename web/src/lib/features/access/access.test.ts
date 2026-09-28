// Component tests of the permission editor (#17): tree selection,
// plain-language actions, risk marks, collapsed rare actions, bulk
// changes with a confirmation, inheritance explanation and the save bar.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
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
	label: 'All resources',
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
	it('shows plain-language actions by type, marks high risk and collapses rare ones', () => {
		render(ActionMatrix, {
			props: { catalog, node: all, mode: 'group', rules: [], onchange: vi.fn() }
		});
		const region = screen.getByRole('region', { name: 'Actions for All resources' });
		expect(within(region).getByRole('heading', { name: 'Containers' })).toBeInTheDocument();
		expect(
			within(region).getByRole('radiogroup', { name: 'Open terminal for All resources' })
		).toHaveTextContent('High risk');
		expect(within(region).getByText('1 less common action')).toBeInTheDocument();
		expect(
			within(region).queryByRole('radiogroup', { name: 'Pause for All resources' })
		).not.toBeVisible();
		expect(within(region).getByText(/including ones added later/)).toBeInTheDocument();
	});

	it('sets a group rule with No rule / Allow / Deny', async () => {
		const user = setup();
		const onchange = vi.fn();
		render(ActionMatrix, { props: { catalog, node: all, mode: 'group', rules: [], onchange } });
		const restart = screen.getByRole('radiogroup', { name: 'Restart for All resources' });
		expect(restart).toHaveTextContent('No rule');
		await user.click(within(restart).getByText('Allow'));
		expect(onchange).toHaveBeenLastCalledWith([
			{ capability: 'container.restart', scope: { kind: 'instance' }, effect: 'allow' }
		]);
	});

	it('changes several actions at once after a confirmation that lists them', async () => {
		const user = setup();
		const onchange = vi.fn();
		render(ActionMatrix, { props: { catalog, node: all, mode: 'group', rules: [], onchange } });
		await user.click(screen.getByRole('checkbox', { name: 'Select Restart' }));
		await user.click(screen.getByRole('checkbox', { name: 'Select Start' }));
		await user.click(screen.getByRole('button', { name: 'Deny' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Deny 2 actions?' });
		expect(dialog).toHaveTextContent('Restart: No rule → Deny');
		expect(dialog).toHaveTextContent('Start: No rule → Deny');
		await user.click(within(dialog).getByRole('button', { name: 'Deny 2 actions' }));
		expect(onchange).toHaveBeenLastCalledWith([
			{ capability: 'container.restart', scope: { kind: 'instance' }, effect: 'deny' },
			{ capability: 'container.start', scope: { kind: 'instance' }, effect: 'deny' }
		]);
	});

	it('clears the selection when another scope is chosen and changes only its actions', async () => {
		const user = setup();
		const onchange = vi.fn();
		const stack = {
			key: 'res:stack::s1',
			label: 'Silo',
			scope: { kind: 'resource' as const, resourceType: 'stack', resourceId: 's1' },
			type: 'stack',
			environmentId: 'e1'
		};
		const { rerender } = render(ActionMatrix, {
			props: { catalog, node: all, mode: 'group', rules: [], onchange }
		});
		await user.click(screen.getByRole('checkbox', { name: 'Select Create stacks' }));
		await user.click(screen.getByRole('checkbox', { name: 'Select Deploy' }));
		await rerender({ catalog, node: stack, mode: 'group', rules: [], onchange });
		expect(screen.queryByRole('checkbox', { name: 'Select Create stacks' })).toBeNull();
		expect(screen.getByRole('checkbox', { name: 'Select Deploy' })).not.toBeChecked();
		expect(screen.queryByRole('group', { name: /selected actions/ })).toBeNull();
		await user.click(screen.getByRole('checkbox', { name: 'Select Deploy' }));
		await user.click(screen.getByRole('button', { name: 'Allow' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Allow 1 action?' });
		await user.click(within(dialog).getByRole('button', { name: 'Allow 1 action' }));
		expect(onchange).toHaveBeenLastCalledWith([
			{ capability: 'stack.deploy', scope: stack.scope, effect: 'allow' }
		]);
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
		const restart = screen.getByRole('radiogroup', { name: 'Restart for All resources' });
		expect(restart).toHaveAccessibleDescription(
			/Inherits Allow from group Operators, rule for everywhere/
		);
		expect(
			screen.getByRole('radiogroup', { name: 'Start for All resources' })
		).toHaveAccessibleDescription(/Inherits Deny from group Operators \(no rule\)/);
		await user.click(within(restart).getByText('Deny'));
		expect(onchange).toHaveBeenLastCalledWith([
			{ capability: 'container.restart', scope: { kind: 'instance' }, effect: 'deny' }
		]);
	});

	it('shows the environment rule a stack inherits though its scope names no environment', () => {
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
		expect(
			screen.getByRole('radiogroup', { name: 'Deploy for Silo' })
		).toHaveAccessibleDescription(
			/Inherits Allow from group Operators, rule for this environment/
		);
	});

	it('offers a token only the actions its user holds (#31)', () => {
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
			screen.getByRole('region', { name: 'Unsaved permission changes' })
		).toHaveTextContent('1 unsaved change');
		await user.click(screen.getByRole('button', { name: 'Save permissions' }));
		const dialog = await screen.findByRole('alertdialog', {
			name: 'Save the permissions of Operators?'
		});
		expect(dialog).toHaveTextContent('Restart (containers) on everything: No rule → Allow');
		await user.click(within(dialog).getByRole('button', { name: 'Save permissions' }));
		expect(onsave).toHaveBeenCalled();
	});
});
