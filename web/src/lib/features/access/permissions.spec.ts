import { describe, expect, it } from 'vitest';
import {
	capabilitiesAt,
	capabilityLabel,
	diffRules,
	effectAt,
	groupCapabilities,
	heldCapabilities,
	inheritedDecision,
	ruleCounts,
	scopeConsequence,
	scopeKey,
	setRule,
	type Catalog,
	type Rule,
	type Scope
} from './permissions';
import {
	accountStatus,
	displayName,
	exceedsMaxLifetime,
	expiryFromDays,
	factorsText,
	groupMembers,
	invitationStatus,
	memberCandidates,
	membersText,
	secondaryName,
	tokenStatus
} from './model';
import { nodesFromRules, resourceNode, serviceNodes } from './tree';

const scopes = (instance: boolean, environment: boolean, resourceTypes: string[] = []) => ({
	instance,
	environment,
	resourceTypes
});
const catalog: Catalog = {
	version: 3,
	resourceTypes: [
		{
			key: 'container',
			label: 'Containers',
			environmentBound: true,
			namedPerEnvironment: true,
			parents: ['service', 'stack'],
			scopable: true
		},
		{
			key: 'stack',
			label: 'Stacks',
			environmentBound: true,
			namedPerEnvironment: false,
			parents: [],
			scopable: true
		},
		{
			key: 'administration',
			label: 'Administration (owner only)',
			environmentBound: false,
			namedPerEnvironment: false,
			parents: [],
			scopable: false
		}
	],
	capabilities: [
		{
			key: 'container.restart',
			label: 'Restart',
			description: 'Restart a container',
			resourceType: 'container',
			risk: 'normal',
			advanced: false,
			ownerOnly: false,
			since: 1,
			scopes: scopes(true, true, ['stack', 'service', 'container'])
		},
		{
			key: 'container.exec',
			label: 'Open terminal',
			description: 'Run commands',
			resourceType: 'container',
			risk: 'high',
			advanced: false,
			ownerOnly: false,
			since: 1,
			scopes: scopes(true, true, ['stack', 'service', 'container'])
		},
		{
			key: 'container.pause',
			label: 'Pause',
			description: 'Pause',
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
			description: 'Deploy a stack',
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
			description: 'Create',
			resourceType: 'stack',
			risk: 'normal',
			advanced: true,
			ownerOnly: false,
			since: 1,
			scopes: scopes(true, true)
		},
		{
			key: 'users.manage',
			label: 'Manage users',
			description: 'Owner',
			resourceType: 'administration',
			risk: 'high',
			advanced: false,
			ownerOnly: true,
			since: 1,
			scopes: scopes(true, false)
		}
	]
};

const I: Scope = { kind: 'instance' };
const E: Scope = { kind: 'environment', environmentId: 'e1' };
const C: Scope = {
	kind: 'resource',
	resourceType: 'container',
	environmentId: 'e1',
	resourceId: 'web'
};
const S: Scope = { kind: 'resource', resourceType: 'stack', resourceId: 's1' };

describe('scopes and capabilities (#17)', () => {
	it('offers only the capabilities a scope supports, never owner-only ones', () => {
		expect(capabilitiesAt(catalog, { scope: I, type: 'instance' }).map((c) => c.key)).toEqual([
			'container.restart',
			'container.exec',
			'container.pause',
			'stack.deploy',
			'stack.create'
		]);
		expect(capabilitiesAt(catalog, { scope: S, type: 'stack' }).map((c) => c.key)).toEqual([
			'container.restart',
			'container.exec',
			'stack.deploy'
		]);
		expect(capabilitiesAt(catalog, { scope: C, type: 'container' }).map((c) => c.key)).toEqual([
			'container.restart',
			'container.exec',
			'container.pause'
		]);
	});

	it('groups by resource type, keeps less common actions apart and filters by name', () => {
		const g = groupCapabilities(
			catalog,
			capabilitiesAt(catalog, { scope: I, type: 'instance' })
		);
		expect(g.map((x) => x.label)).toEqual(['Containers', 'Stacks']);
		expect(g[0].common.map((c) => c.key)).toEqual(['container.restart', 'container.exec']);
		expect(g[0].advanced.map((c) => c.key)).toEqual(['container.pause']);
		expect(
			groupCapabilities(catalog, catalog.capabilities, 'terminal')[0].common.map((c) => c.key)
		).toEqual(['container.exec']);
	});

	it('names capabilities in plain language', () => {
		expect(capabilityLabel(catalog, 'container.restart')).toBe('Restart (containers)');
		expect(capabilityLabel(catalog, 'stack.create')).toBe('Create stacks');
		expect(capabilityLabel(catalog, 'nope.x')).toBe('nope.x');
	});

	it('explains what a rule at each scope reaches, including future resources', () => {
		expect(scopeConsequence({ scope: I, type: 'instance', label: 'All' })).toMatch(
			/including ones added later.*never included automatically/
		);
		expect(
			scopeConsequence({ scope: E, type: 'environment', label: 'homelab' }, 'homelab')
		).toMatch(/on homelab, including ones created later/);
		expect(scopeConsequence({ scope: S, type: 'stack', label: 'Silo' })).toMatch(
			/service containers, current and future/
		);
		expect(scopeConsequence({ scope: C, type: 'container', label: 'web' })).toBe(
			'Rules here apply to web only.'
		);
	});
});

describe('rules', () => {
	it('sets, changes and clears one rule per capability and scope', () => {
		let rules: Rule[] = [];
		rules = setRule(rules, 'container.restart', E, 'allow');
		rules = setRule(rules, 'container.restart', C, 'deny');
		rules = setRule(rules, 'container.restart', E, 'deny');
		expect(effectAt(rules, 'container.restart', E)).toBe('deny');
		expect(rules).toHaveLength(2);
		rules = setRule(rules, 'container.restart', C, null);
		expect(effectAt(rules, 'container.restart', C)).toBeNull();
		expect(scopeKey(C)).toBe('res:container:e1:web');
	});

	it('diffs documents for the confirmation and ignores unchanged rules', () => {
		const before: Rule[] = [
			{ capability: 'container.restart', effect: 'allow', scope: E },
			{ capability: 'stack.deploy', effect: 'allow', scope: I }
		];
		const after: Rule[] = [
			{ capability: 'stack.deploy', effect: 'allow', scope: I },
			{ capability: 'container.restart', effect: 'deny', scope: E },
			{ capability: 'container.exec', effect: 'allow', scope: C }
		];
		expect(diffRules(before, after)).toEqual([
			{ capability: 'container.exec', scope: C, before: null, after: 'allow' },
			{ capability: 'container.restart', scope: E, before: 'allow', after: 'deny' }
		]);
		expect(diffRules(after, after)).toEqual([]);
	});

	it('counts rules per scope and rolls resources up to their environment', () => {
		const c = ruleCounts([
			{ capability: 'container.restart', effect: 'allow', scope: C },
			{ capability: 'container.exec', effect: 'deny', scope: C },
			{ capability: 'stack.deploy', effect: 'allow', scope: E },
			{ capability: 'stack.deploy', effect: 'allow', scope: I }
		]);
		expect(c.get('res:container:e1:web')).toBe(2);
		expect(c.get('env:e1')).toBe(1);
		expect(c.get('env-all:e1')).toBe(3);
		expect(c.get('instance')).toBe(1);
	});

	it('inherits the most specific group rule: resource, then environment, then everything, else deny', () => {
		const group: Rule[] = [
			{ capability: 'container.restart', effect: 'allow', scope: I },
			{ capability: 'container.restart', effect: 'deny', scope: E }
		];
		expect(inheritedDecision(group, 'container.restart', C)).toEqual({
			effect: 'deny',
			at: 'environment'
		});
		expect(
			inheritedDecision(group, 'container.restart', {
				kind: 'environment',
				environmentId: 'e2'
			})
		).toEqual({
			effect: 'allow',
			at: 'instance'
		});
		expect(
			inheritedDecision(
				[...group, { capability: 'container.restart', effect: 'allow', scope: C }],
				'container.restart',
				C
			)
		).toEqual({
			effect: 'allow',
			at: 'resource'
		});
		expect(inheritedDecision(group, 'container.exec', C)).toEqual({
			effect: 'deny',
			at: 'none'
		});
	});

	it('inherits the environment rule on a stack from the environment it is listed under', () => {
		const group: Rule[] = [{ capability: 'stack.deploy', effect: 'allow', scope: E }];
		expect(inheritedDecision(group, 'stack.deploy', S, 'e1')).toEqual({
			effect: 'allow',
			at: 'environment'
		});
		expect(inheritedDecision(group, 'stack.deploy', S)).toEqual({ effect: 'deny', at: 'none' });
	});

	it('limits token grants to what the caller holds; the owner holds every grantable key (#31)', () => {
		expect([
			...heldCapabilities(
				[
					{
						capability: 'container.restart',
						allowed: true,
						reason: '',
						scope: C,
						source: 'group_rule'
					},
					{
						capability: 'container.exec',
						allowed: false,
						reason: '',
						scope: C,
						source: 'user_rule'
					}
				],
				false
			)
		]).toEqual(['container.restart']);
		expect(heldCapabilities([], true, catalog).has('users.manage')).toBe(false);
		expect(heldCapabilities([], true, catalog).has('stack.deploy')).toBe(true);
	});
});

describe('resource tree', () => {
	it('keeps rules on resources that are not listed any more', () => {
		const rules: Rule[] = [
			{ capability: 'container.restart', effect: 'allow', scope: C },
			{
				capability: 'container.restart',
				effect: 'allow',
				scope: { ...C, resourceId: 'gone' }
			},
			{ capability: 'stack.deploy', effect: 'allow', scope: S }
		];
		const nodes = nodesFromRules(rules, 'container', 'e1', new Set(['res:container:e1:web']));
		expect(nodes.map((n) => [n.label, n.detail])).toEqual([['gone', 'Not listed now']]);
	});

	it('shows a rule on a stack that no environment lists, once, without an environment', () => {
		const rules: Rule[] = [
			{ capability: 'stack.deploy', effect: 'allow', scope: S },
			{ capability: 'stack.deploy', effect: 'allow', scope: { ...S, resourceId: 'gone' } },
			{
				capability: 'container.restart',
				effect: 'allow',
				scope: { ...S, resourceId: 'gone' }
			}
		];
		const nodes = nodesFromRules(rules, 'stack', 'e2', new Set([scopeKey(S)]));
		expect(nodes.map((n) => [n.key, n.label, n.detail])).toEqual([
			['res:stack::gone', 'gone', 'Not listed here']
		]);
		expect(nodes[0].scope).toEqual({
			kind: 'resource',
			resourceType: 'stack',
			resourceId: 'gone'
		});
		expect(nodes[0].environmentId).toBeUndefined();
	});

	it("shows an unlisted stack or service rule only under its stack's environment", () => {
		const rules: Rule[] = [
			{ capability: 'stack.deploy', effect: 'allow', scope: { ...S, resourceId: 'down' } },
			{
				capability: 'stack.restart',
				effect: 'allow',
				scope: { kind: 'resource', resourceType: 'service', resourceId: 'down/web' }
			}
		];
		const home = (type: string, id: string) =>
			(type === 'stack' ? id : id.split('/')[0]) === 'down' ? 'e1' : undefined;
		expect(nodesFromRules(rules, 'stack', 'e2', new Set(), home)).toEqual([]);
		expect(nodesFromRules(rules, 'service', 'e2', new Set(), home)).toEqual([]);
		expect(nodesFromRules(rules, 'stack', 'e1', new Set(), home).map((n) => n.label)).toEqual([
			'down'
		]);
		expect(nodesFromRules(rules, 'service', 'e1', new Set(), home).map((n) => n.label)).toEqual(
			['down/web']
		);
	});

	it('names services by stack, without an environment in the scope', () => {
		const [web] = serviceNodes('s1', [{ name: 'web' }], 'e1');
		expect(web.scope).toEqual({
			kind: 'resource',
			resourceType: 'service',
			resourceId: 's1/web'
		});
		expect(web.environmentId).toBe('e1');
		expect(web.key).toBe(scopeKey(web.scope));
	});

	it('puts the environment in the scope only for types named per environment', () => {
		for (const type of ['container', 'image', 'volume', 'network']) {
			const n = resourceNode(type, 'x', 'e1', 'x');
			expect(n.scope).toEqual({
				kind: 'resource',
				resourceType: type,
				resourceId: 'x',
				environmentId: 'e1'
			});
			expect(n.key).toBe(scopeKey(n.scope));
		}
		for (const type of ['stack', 'service', 'agent', 'update_policy', 'maintenance_policy']) {
			const n = resourceNode(type, 'x', 'e1', 'x');
			expect(n.scope).toEqual({ kind: 'resource', resourceType: type, resourceId: 'x' });
			expect(n.environmentId).toBe('e1');
			expect(n.key).toBe(scopeKey(n.scope));
		}
	});
});

describe('accounts and tokens', () => {
	const f = { password: true, totp: true, passkeys: 2, recoveryCodesRemaining: 8 };
	it('summarizes accounts', () => {
		expect(displayName({ displayName: ' ', username: 'guest' })).toBe('guest');
		expect(factorsText(f)).toBe('Password, authenticator app and 2 passkeys');
		expect(factorsText({ ...f, passkeys: 0 })).toBe('Password and authenticator app');
		expect(factorsText({ ...f, password: false, totp: false, passkeys: 1 })).toBe('1 passkey');
		expect(factorsText({ ...f, password: false, totp: false, passkeys: 0 })).toBe('None');
		expect(secondaryName({ displayName: 'Juxnist', username: 'juxnist' })).toBeUndefined();
		expect(
			secondaryName({ displayName: 'Juxnist', username: 'juxnist', email: 'j@example.com' })
		).toBe('j@example.com');
		expect(secondaryName({ displayName: 'Ada Lovelace', username: 'ada' })).toBe('ada');
		expect(secondaryName({ displayName: '', username: 'ada' })).toBeUndefined();
		const users = [
			{ id: 'o', groupId: 'g1', owner: true },
			{ id: 'a', groupId: 'g1', owner: false },
			{ id: 'b', groupId: 'g2', owner: false }
		];
		expect(groupMembers(users, 'g1').map((u) => u.id)).toEqual(['a']);
		expect(groupMembers(undefined, 'g1')).toEqual([]);
		expect(membersText(1)).toBe('1 member');
		expect(membersText(0)).toBe('0 members');
		const people = [
			{ groupId: 'g1', owner: true, displayName: 'Owner', username: 'own' },
			{ groupId: 'g1', owner: false, displayName: 'Ada', username: 'ada' },
			{ groupId: 'g2', owner: false, displayName: 'Zoe', username: 'zoe' },
			{ groupId: 'g2', owner: false, displayName: '', username: 'bob', email: 'b@lab.test' }
		];
		expect(memberCandidates(people, 'g1').map((u) => u.username)).toEqual(['bob', 'zoe']);
		expect(memberCandidates(people, 'g1', 'LAB').map((u) => u.username)).toEqual(['bob']);
		expect(memberCandidates(people, 'g2').map((u) => u.username)).toEqual(['ada']);
		expect(accountStatus({ status: 'disabled', owner: false }).label).toBe('Disabled');
		expect(
			accountStatus({ status: 'active', owner: false, enrollmentDeadline: 'x' }).label
		).toBe('Enrolling factors');
		expect(invitationStatus('pending').label).toBe('Pending');
		expect(tokenStatus('revoked').label).toBe('Revoked');
	});

	it('computes token expiry within the instance maximum', () => {
		expect(expiryFromDays(30, new Date('2026-09-25T00:00:00Z'))).toBe(
			'2026-10-25T00:00:00.000Z'
		);
		expect(exceedsMaxLifetime(120, 90)).toBe(true);
		expect(exceedsMaxLifetime(90, 90)).toBe(false);
		expect(exceedsMaxLifetime(365, undefined)).toBe(false);
	});
});
