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
	/**
	 * The environment the node is listed under. Resources with a global ID
	 * (stacks, services, agents, policies) keep it here, not in the scope.
	 */
	environmentId?: string;
	/** Child scopes (a stack's services). */
	children?: ScopeNode[];
	/**
	 * The scopes containing the resource, nearest first (a service's stack;
	 * a container's service and stack): their rules apply to it too.
	 */
	parents?: Scope[];
}

export const INSTANCE: Scope = { kind: 'instance' };

/** Stable key of a scope (IDs are unique per type, Docker names per type and environment). */
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

/**
 * Editor sections that hold several resource types (#281), so related
 * actions sit together: an environment's agents with it, the backup
 * repositories, settings and backups as Backups, the registry connections
 * and Git credentials as Credentials. Other types are their own section.
 */
const SECTIONS: Record<string, { key: string; label: string }> = {
	environment: { key: 'environment', label: 'Environments' },
	agent: { key: 'environment', label: 'Environments' },
	backup_repository: { key: 'backup', label: 'Backups' },
	backup_policy: { key: 'backup', label: 'Backups' },
	backup: { key: 'backup', label: 'Backups' },
	registry: { key: 'credentials', label: 'Credentials' },
	git_credential: { key: 'credentials', label: 'Credentials' }
};

/**
 * Capabilities in editor sections (resource types, some merged: SECTIONS)
 * in catalog order, less common ones apart (#17 UX).
 */
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
		const section = SECTIONS[t.key] ?? { key: t.key, label: t.label };
		let g = out.find((x) => x.type === section.key);
		if (!g) {
			g = { type: section.key, label: section.label, common: [], advanced: [] };
			out.push(g);
		}
		g.common.push(...list.filter((c) => !c.advanced));
		g.advanced.push(...list.filter((c) => c.advanced));
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
			return 'Applies to every environment, including ones added later. Future actions are never included automatically.';
		case 'environment':
			return `Applies to everything on ${environmentName ?? 'this environment'}, including ones created later.`;
	}
	switch (node.type) {
		case 'stack':
			return `Applies to ${node.label}. Container actions also cover its service containers, current and future.`;
		case 'service':
			return `Applies to ${node.label} alone: its Start, Stop and Restart, and its containers, current and future.`;
		default:
			return `Applies to ${node.label} only.`;
	}
}

/** A resource type in words, for scopes ("update_policy" → "update target"). */
const SCOPE_TYPES: Record<string, string> = {
	stack: 'stack',
	service: 'service',
	container: 'container',
	image: 'image',
	volume: 'volume',
	network: 'network',
	agent: 'agent',
	update_policy: 'update target',
	build_definition: 'build definition',
	backup_repository: 'backup repository',
	backup: 'backup',
	registry: 'registry connection',
	git_credential: 'Git credential',
	template: 'template'
};

/**
 * Plain-language scope, e.g. "All Resources", "homelab", "stack Silo on
 * homelab": names come from `names` (the tree's nodes), else the resource
 * ID, shortened when it is a long generated one; a service names its stack.
 */
export function scopeLabel(
	s: Scope,
	names: {
		environment?: (id: string) => string | undefined;
		resource?: (s: Scope) => string | undefined;
	} = {}
): string {
	if (s.kind === 'instance') return 'All Resources';
	const env = s.environmentId ? (names.environment?.(s.environmentId) ?? s.environmentId) : '';
	if (s.kind === 'environment') return env;
	const type = s.resourceType ?? '';
	const id = s.resourceId ?? '';
	let res: string | undefined;
	if (type === 'service') {
		const [stackId, service] = id.split('/');
		const stack =
			names.resource?.({ kind: 'resource', resourceType: 'stack', resourceId: stackId }) ??
			shortId(stackId);
		res = `${service} of stack ${stack}`;
	}
	res ??= names.resource?.(s) ?? shortId(id);
	return `${SCOPE_TYPES[type] ?? type} ${res}${env ? ` on ${env}` : ''}`;
}

/** A generated ID (UUID, digest) shortened for reading; names stay whole. */
function shortId(id: string): string {
	const bare = id.replace(/^sha256:/, '');
	return /^[0-9a-f-]{20,}$/i.test(bare) ? `${bare.slice(0, 8)}…` : id;
}

/** The capability's label from the catalog ("Restart Containers"), else its key. */
export function capabilityLabel(catalog: Catalog | undefined, key: string): string {
	return catalog?.capabilities.find((x) => x.key === key)?.label ?? key;
}

/** Where the deciding rule of an inherited decision is. */
export type InheritedAt = 'resource' | 'parent' | 'environment' | 'instance' | 'none';

/**
 * The group decision a user inherits at a scope when they have no override
 * (#17 precedence, like the server: the most specific group rule wins: the
 * resource, then its parents nearest first (a container's service, then its
 * stack), then its environment, then instance; no rule means deny). A
 * resource with a global ID names no environment in its scope: pass the
 * node's environment. `parent` names the parent scope that decided.
 */
export function inheritedDecision(
	groupRules: Rule[],
	capability: string,
	scope: Scope,
	environmentId?: string,
	parents: readonly Scope[] = []
): { effect: Effect; at: InheritedAt; parent?: Scope } {
	const exact = effectAt(groupRules, capability, scope);
	if (scope.kind === 'resource' && exact) return { effect: exact, at: 'resource' };
	if (scope.kind === 'resource')
		for (const p of parents) {
			const e = effectAt(groupRules, capability, p);
			if (e) return { effect: e, at: 'parent', parent: p };
		}
	const envId = scope.environmentId ?? environmentId;
	if (scope.kind !== 'instance' && envId) {
		const env = effectAt(groupRules, capability, {
			kind: 'environment',
			environmentId: envId
		});
		if (scope.kind === 'environment' && exact) return { effect: exact, at: 'environment' };
		if (env) return { effect: env, at: 'environment' };
	}
	const inst = effectAt(groupRules, capability, INSTANCE);
	if (inst) return { effect: inst, at: 'instance' };
	return { effect: 'deny', at: 'none' };
}

/** One of a user's groups for what the user inherits: its name and rules. */
export interface InheritedGroup {
	name: string;
	rules: Rule[];
}

/**
 * The decision a user inherits at a scope from their groups when they have
 * no override (#233): the first group, in priority order, with a rule for
 * the capability that applies there decides with its most specific rule;
 * without one, deny. `group` names the deciding group.
 */
export function inheritedFromGroups(
	groups: readonly InheritedGroup[],
	capability: string,
	scope: Scope,
	environmentId?: string,
	parents: readonly Scope[] = []
): { effect: Effect; at: InheritedAt; parent?: Scope; group?: string } {
	for (const g of groups) {
		const d = inheritedDecision(g.rules, capability, scope, environmentId, parents);
		if (d.at !== 'none') return { ...d, group: g.name };
	}
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
