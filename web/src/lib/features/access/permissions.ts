// Permission editing (#17, #31): scopes, rules and the capability catalog
// as the editors need them. Pure functions (permissions.spec.ts); the
// manager validates every document and decides every request.
import type { Schema } from '$lib/api/client';

export type Catalog = Schema<'PermissionCatalog'>;
export type Capability = Schema<'CatalogCapability'>;
export type ResourceType = Schema<'CatalogResourceType'>;
export type Rule = Schema<'PermissionRule'>;
export type Scope = Schema<'PermissionScope'>;
export type Effective = Schema<'EffectivePermission'>;
export type Effect = Rule['effect'];

/** A node of the resource tree: the scope a rule would target. */
export interface ScopeNode {
	key: string;
	label: string;
	scope: Scope;
	/** The catalog type of a resource node (instance/environment otherwise). */
	type: string;
	/** Secondary text (environment name, image, …). */
	detail?: string;
	/** Child scopes (a stack's services). */
	children?: ScopeNode[];
}

export const INSTANCE: Scope = { kind: 'instance' };

/** Stable key of a scope (resource IDs are unique per type and environment). */
export function scopeKey(s: Scope): string {
	if (s.kind === 'instance') return 'instance';
	if (s.kind === 'environment') return `env:${s.environmentId ?? ''}`;
	return `res:${s.resourceType ?? ''}:${s.environmentId ?? ''}:${s.resourceId ?? ''}`;
}

export function ruleKey(capability: string, s: Scope): string {
	return `${capability}|${scopeKey(s)}`;
}

/** Capabilities a rule may grant at a node (owner-only keys never). */
export function capabilitiesAt(
	catalog: Catalog,
	node: Pick<ScopeNode, 'scope' | 'type'>
): Capability[] {
	return catalog.capabilities.filter((c) => {
		if (c.ownerOnly) return false;
		if (node.scope.kind === 'instance') return c.scopes.instance;
		if (node.scope.kind === 'environment') return c.scopes.environment;
		return c.scopes.resourceTypes.includes(node.type);
	});
}

export interface CapabilityGroup {
	type: string;
	label: string;
	common: Capability[];
	advanced: Capability[];
}

/** Capabilities grouped by resource type, less common ones apart (#17 UX). */
export function groupCapabilities(
	catalog: Catalog,
	caps: Capability[],
	filter = ''
): CapabilityGroup[] {
	const q = filter.trim().toLowerCase();
	const match = (c: Capability) =>
		!q ||
		c.label.toLowerCase().includes(q) ||
		c.key.includes(q) ||
		c.description.toLowerCase().includes(q);
	const out: CapabilityGroup[] = [];
	for (const t of catalog.resourceTypes) {
		const list = caps.filter((c) => c.resourceType === t.key && match(c));
		if (!list.length) continue;
		out.push({
			type: t.key,
			label: t.label,
			common: list.filter((c) => !c.advanced),
			advanced: list.filter((c) => c.advanced)
		});
	}
	return out;
}

/** Rules as a map from ruleKey to effect (for editing). */
export function toMap(rules: Rule[]): Map<string, Rule> {
	return new Map(rules.map((r) => [ruleKey(r.capability, r.scope), r]));
}

/** Set, change or clear (effect null) one rule; returns a new list. */
export function setRule(
	rules: Rule[],
	capability: string,
	scope: Scope,
	effect: Effect | null
): Rule[] {
	const k = ruleKey(capability, scope);
	const rest = rules.filter((r) => ruleKey(r.capability, r.scope) !== k);
	return effect ? [...rest, { capability, scope, effect }] : rest;
}

export function effectAt(rules: Rule[], capability: string, scope: Scope): Effect | null {
	const k = ruleKey(capability, scope);
	return rules.find((r) => ruleKey(r.capability, r.scope) === k)?.effect ?? null;
}

export interface RuleChange {
	capability: string;
	scope: Scope;
	before: Effect | null;
	after: Effect | null;
}

/** What saving would change (the confirmation and the audit diff). */
export function diffRules(before: Rule[], after: Rule[]): RuleChange[] {
	const a = toMap(before);
	const b = toMap(after);
	const keys = [...new Set([...a.keys(), ...b.keys()])].sort();
	const out: RuleChange[] = [];
	for (const k of keys) {
		const x = a.get(k);
		const y = b.get(k);
		if (x?.effect === y?.effect) continue;
		const r = (y ?? x)!;
		out.push({
			capability: r.capability,
			scope: r.scope,
			before: x?.effect ?? null,
			after: y?.effect ?? null
		});
	}
	return out;
}

/** Rule counts per scope key, and per environment including its resources. */
export function ruleCounts(rules: Rule[]): Map<string, number> {
	const m = new Map<string, number>();
	const add = (k: string) => m.set(k, (m.get(k) ?? 0) + 1);
	for (const r of rules) {
		add(scopeKey(r.scope));
		if (r.scope.kind === 'resource' && r.scope.environmentId)
			add(`env-all:${r.scope.environmentId}`);
		if (r.scope.kind === 'environment') add(`env-all:${r.scope.environmentId}`);
	}
	return m;
}

/**
 * What a rule at this scope reaches (#17 dynamic scopes): wildcards cover
 * future resources of the type, never future capability keys.
 */
export function scopeConsequence(
	node: Pick<ScopeNode, 'scope' | 'type' | 'label'>,
	environmentName?: string
): string {
	switch (node.scope.kind) {
		case 'instance':
			return 'Rules here apply to every resource of their type in every environment, including ones added later. Actions DockYard adds in the future are never included automatically.';
		case 'environment':
			return `Rules here apply to every resource of their type on ${environmentName ?? 'this environment'}, including ones created later.`;
	}
	switch (node.type) {
		case 'stack':
			return `Rules here apply to ${node.label}. Container actions chosen here also cover its service containers, current and future (recreated ones too).`;
		case 'service':
			return `Rules here apply to the containers of the service ${node.label}, current and future.`;
		default:
			return `Rules here apply to ${node.label} only.`;
	}
}

/** Plain-language scope, e.g. "all environments", "homelab", "stack Silo on homelab". */
export function scopeLabel(
	s: Scope,
	names: { environment?: (id: string) => string; resource?: (s: Scope) => string } = {}
): string {
	if (s.kind === 'instance') return 'everything';
	const env = s.environmentId ? (names.environment?.(s.environmentId) ?? s.environmentId) : '';
	if (s.kind === 'environment') return env;
	const res = names.resource?.(s) ?? s.resourceId ?? '';
	return `${s.resourceType} ${res}${env ? ` on ${env}` : ''}`;
}

/** The capability's label from the catalog ("Restart"), else its key. */
export function capabilityLabel(catalog: Catalog | undefined, key: string): string {
	const c = catalog?.capabilities.find((x) => x.key === key);
	if (!c) return key;
	const t = catalog?.resourceTypes.find((x) => x.key === c.resourceType);
	return t && !c.label.toLowerCase().includes(t.label.toLowerCase().replace(/s$/, ''))
		? `${c.label} (${t.label.toLowerCase()})`
		: c.label;
}

/**
 * The group decision a user inherits at a scope when they have no override
 * (#17 precedence: the most specific group rule wins: resource, then its
 * environment, then instance; no rule means deny).
 */
export function inheritedDecision(
	groupRules: Rule[],
	capability: string,
	scope: Scope
): { effect: Effect; at: 'resource' | 'environment' | 'instance' | 'none' } {
	const exact = effectAt(groupRules, capability, scope);
	if (scope.kind === 'resource' && exact) return { effect: exact, at: 'resource' };
	if (scope.kind !== 'instance' && scope.environmentId) {
		const env = effectAt(groupRules, capability, {
			kind: 'environment',
			environmentId: scope.environmentId
		});
		if (scope.kind === 'environment' && exact) return { effect: exact, at: 'environment' };
		if (env) return { effect: env, at: 'environment' };
	}
	const inst = effectAt(groupRules, capability, INSTANCE);
	if (inst) return { effect: inst, at: 'instance' };
	return { effect: 'deny', at: 'none' };
}

/** Capabilities the caller holds anywhere (the token scope editor's limit, #31). */
export function heldCapabilities(
	entries: Effective[] | undefined,
	owner: boolean,
	catalog?: Catalog
): Set<string> {
	if (owner && catalog)
		return new Set(catalog.capabilities.filter((c) => !c.ownerOnly).map((c) => c.key));
	return new Set((entries ?? []).filter((e) => e.allowed).map((e) => e.capability));
}
