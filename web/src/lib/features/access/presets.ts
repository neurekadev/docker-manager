// Starting points of the permission editor (#17 groups, #31 API tokens):
// Viewer, Operator and Admin, derived from the catalog's own data (the
// action's last key segment, its risk and whether it is a less common
// action), never from a hand-kept list of keys, so actions the catalog
// adds later fall into place. A preset only changes the unsaved draft at
// one scope; the save bar previews every change before anything is saved.
// Pure (presets.spec.ts).
import {
	scopeKey,
	setRule,
	type Capability,
	type Effect,
	type Rule,
	type Scope
} from './permissions';

export type PresetId = 'viewer' | 'operator' | 'admin';

export interface Preset {
	id: PresetId;
	label: string;
	description: string;
}

export const PRESETS: readonly Preset[] = [
	{
		id: 'viewer',
		label: 'Viewer',
		description: 'Sees everything, changes nothing. Logs, files and secrets stay hidden.'
	},
	{
		id: 'operator',
		label: 'Operator',
		description:
			'Viewer, plus start, stop, restart and deploy, pull images, run backups and read logs. Nothing is deleted or administered.'
	},
	{
		id: 'admin',
		label: 'Admin',
		description: 'Every action offered here, high-risk ones included.'
	}
];

/** The action only reads (its key ends in "read": stack.read, container.metrics.read). */
export function isReadAction(c: Pick<Capability, 'key'>): boolean {
	return c.key.split('.').pop() === 'read';
}

/** Reading container logs (high risk: logs can hold secrets, but operators need them). */
function isLogs(c: Pick<Capability, 'key'>): boolean {
	return c.key.endsWith('.logs.read');
}

/** Whether a preset grants the action. */
export function presetIncludes(
	id: PresetId,
	c: Pick<Capability, 'key' | 'risk' | 'advanced'>
): boolean {
	const view = isReadAction(c) && c.risk !== 'high';
	switch (id) {
		case 'viewer':
			return view;
		case 'operator':
			return view || isLogs(c) || (c.risk !== 'high' && !c.advanced);
		case 'admin':
			return true;
	}
}

/**
 * The rules after starting from a preset at one scope: every action in
 * `available` (what the scope offers) is allowed when the preset grants
 * it and has no rule otherwise; rules at other scopes and of other
 * actions stay as they are.
 */
export function applyPreset(
	rules: Rule[],
	id: PresetId,
	scope: Scope,
	available: readonly Capability[]
): Rule[] {
	let next = rules;
	for (const c of available)
		next = setRule(next, c.key, scope, presetIncludes(id, c) ? 'allow' : null);
	return next;
}

/**
 * The preset the rules at this scope match exactly, 'custom' when they
 * match none, null when the scope has no rule for these actions yet.
 */
export function matchingPreset(
	rules: readonly Rule[],
	scope: Scope,
	available: readonly Capability[]
): PresetId | 'custom' | null {
	const at = scopeKey(scope);
	const keys = new Set(available.map((c) => c.key));
	const here = rules.filter((r) => scopeKey(r.scope) === at && keys.has(r.capability));
	if (!here.length) return null;
	if (here.some((r) => r.effect !== 'allow')) return 'custom';
	const allowed = new Set(here.map((r) => r.capability));
	for (const p of PRESETS) {
		const want = available.filter((c) => presetIncludes(p.id, c));
		if (want.length === allowed.size && want.every((c) => allowed.has(c.key))) return p.id;
	}
	return 'custom';
}

/** Sets (or clears, effect null) the same rule for several actions at one scope. */
export function setMany(
	rules: Rule[],
	capabilities: readonly Pick<Capability, 'key'>[],
	scope: Scope,
	effect: Effect | null
): Rule[] {
	let next = rules;
	for (const c of capabilities) next = setRule(next, c.key, scope, effect);
	return next;
}

/** How many of these actions are allowed and denied at the scope (a section's summary). */
export function sectionCounts(
	rules: readonly Rule[],
	scope: Scope,
	capabilities: readonly Pick<Capability, 'key'>[]
): { allow: number; deny: number } {
	const at = scopeKey(scope);
	const keys = new Set(capabilities.map((c) => c.key));
	let allow = 0;
	let deny = 0;
	for (const r of rules) {
		if (scopeKey(r.scope) !== at || !keys.has(r.capability)) continue;
		if (r.effect === 'allow') allow++;
		else deny++;
	}
	return { allow, deny };
}
