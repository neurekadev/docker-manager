import { describe, expect, it } from 'vitest';
import type { Capability, Rule, Scope } from './permissions';
import {
	applyPreset,
	isReadAction,
	matchingPreset,
	presetIncludes,
	sectionCounts,
	setMany
} from './presets';

const cap = (key: string, risk: 'normal' | 'high' = 'normal', advanced = false): Capability => ({
	key,
	label: key,
	description: '',
	resourceType: key.split('.')[0],
	risk,
	advanced,
	ownerOnly: false,
	since: 1,
	scopes: { instance: true, environment: true, resourceTypes: [] }
});

const read = cap('stack.read');
const metrics = cap('container.metrics.read');
const settingsRead = cap('settings.read', 'normal', true);
const definition = cap('stack.definition.read', 'high');
const logs = cap('container.logs.read', 'high');
const deploy = cap('stack.deploy');
const build = cap('stack.build', 'normal', true);
const remove = cap('stack.remove', 'high', true);
const exec = cap('container.exec', 'high');
const all = [read, metrics, settingsRead, definition, logs, deploy, build, remove, exec];
const instance: Scope = { kind: 'instance' };
const env: Scope = { kind: 'environment', environmentId: 'e1' };

describe('permission presets', () => {
	it('recognizes read actions by the last segment of their key', () => {
		expect(isReadAction(read)).toBe(true);
		expect(isReadAction(metrics)).toBe(true);
		expect(isReadAction(cap('stack.readme'))).toBe(false);
		expect(isReadAction(deploy)).toBe(false);
	});

	it('derives Viewer, Operator and Admin from the catalog flags', () => {
		const keys = (id: 'viewer' | 'operator' | 'admin') =>
			all.filter((c) => presetIncludes(id, c)).map((c) => c.key);
		// Viewer: every normal-risk read, less common ones too; no secrets.
		expect(keys('viewer')).toEqual(['stack.read', 'container.metrics.read', 'settings.read']);
		// Operator: plus the common normal-risk actions and container logs;
		// nothing high risk or rare (removal, builds).
		expect(keys('operator')).toEqual([
			'stack.read',
			'container.metrics.read',
			'settings.read',
			'container.logs.read',
			'stack.deploy'
		]);
		expect(keys('admin')).toEqual(all.map((c) => c.key));
	});

	it('sets the rules of one scope and keeps the others', () => {
		const other: Rule = { capability: 'stack.deploy', scope: env, effect: 'deny' };
		const before: Rule[] = [
			other,
			{ capability: 'stack.remove', scope: instance, effect: 'allow' },
			{ capability: 'stack.read', scope: instance, effect: 'deny' }
		];
		const after = applyPreset(before, 'viewer', instance, all);
		expect(after).toContainEqual(other);
		expect(
			after
				.filter((r) => r.scope.kind === 'instance')
				.map((r) => `${r.effect} ${r.capability}`)
				.sort()
		).toEqual(['allow container.metrics.read', 'allow settings.read', 'allow stack.read']);
	});

	it('names the preset the rules of a scope match', () => {
		expect(matchingPreset([], instance, all)).toBeNull();
		for (const id of ['viewer', 'operator', 'admin'] as const)
			expect(matchingPreset(applyPreset([], id, instance, all), instance, all)).toBe(id);
		const viewer = applyPreset([], 'viewer', instance, all);
		expect(matchingPreset(viewer, env, all)).toBeNull();
		expect(
			matchingPreset(
				[...viewer, { capability: 'stack.deploy', scope: instance, effect: 'deny' }],
				instance,
				all
			)
		).toBe('custom');
		expect(matchingPreset(viewer.slice(1), instance, all)).toBe('custom');
	});

	it('changes a whole section and counts its rules', () => {
		let rules = setMany([], [read, deploy], instance, 'allow');
		rules = setMany(rules, [exec], instance, 'deny');
		rules = setMany(rules, [deploy], env, 'allow');
		expect(sectionCounts(rules, instance, [read, deploy, exec, logs])).toEqual({
			allow: 2,
			deny: 1
		});
		expect(sectionCounts(rules, instance, [read])).toEqual({ allow: 1, deny: 0 });
		rules = setMany(rules, [read, deploy, exec], instance, null);
		expect(rules).toEqual([{ capability: 'stack.deploy', scope: env, effect: 'allow' }]);
	});
});
