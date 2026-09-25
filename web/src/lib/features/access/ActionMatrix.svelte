<script lang="ts">
	// The actions of one scope (#17): plain-language capability names
	// grouped by resource type, high-risk ones marked, less common ones
	// collapsed. Groups set Allow or Deny (no rule = deny); users override
	// with Inherit / Allow / Deny and see what they inherit; API tokens
	// (#31) grant a subset of what the caller holds. Several actions can be
	// changed at once after a confirmation preview.
	import {
		Badge,
		Button,
		Checkbox,
		ConfirmDialog,
		TextField,
		TriState,
		type TriValue
	} from '$lib/ui';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import {
		capabilitiesAt,
		effectAt,
		groupCapabilities,
		inheritedDecision,
		scopeConsequence,
		setRule,
		type Capability,
		type Catalog,
		type Effect,
		type Rule,
		type ScopeNode
	} from './permissions';

	interface Props {
		catalog: Catalog;
		node: ScopeNode;
		mode: 'group' | 'user' | 'token';
		rules: Rule[];
		/** User mode: the group's rules, for what is inherited. */
		groupRules?: Rule[];
		groupName?: string;
		/** Token mode: capabilities the caller holds. */
		held?: ReadonlySet<string>;
		readonly?: boolean;
		environmentName?: (id: string) => string;
		onchange: (rules: Rule[]) => void;
	}

	let {
		catalog,
		node,
		mode,
		rules,
		groupRules = [],
		groupName,
		held,
		readonly = false,
		environmentName,
		onchange
	}: Props = $props();

	let filter = $state('');
	let picked = $state<string[]>([]);
	let bulk = $state<{ effect: Effect | null; label: string } | null>(null);
	let bulkOpen = $state(false);

	const available = $derived(
		capabilitiesAt(catalog, node).filter((c) => mode !== 'token' || !held || held.has(c.key))
	);
	const groups = $derived(groupCapabilities(catalog, available, filter));
	const envLabel = $derived(
		node.scope.environmentId ? environmentName?.(node.scope.environmentId) : undefined
	);

	function value(c: Capability): TriValue {
		return effectAt(rules, c.key, node.scope) ?? 'inherit';
	}

	function set(c: Capability, v: TriValue | null) {
		onchange(setRule(rules, c.key, node.scope, v === 'inherit' || v === null ? null : v));
	}

	function inherited(c: Capability) {
		return inheritedDecision(groupRules, c.key, node.scope);
	}

	function inheritedText(c: Capability): string {
		const d = inherited(c);
		if (d.at === 'none') return `group ${groupName ?? ''} (no rule)`.replace('  ', ' ');
		const where =
			d.at === 'instance'
				? 'everywhere'
				: d.at === 'environment'
					? 'this environment'
					: 'this resource';
		return `group ${groupName ?? ''}, rule for ${where}`.replace('  ', ' ');
	}

	function togglePick(key: string, on: boolean) {
		picked = on ? [...picked, key] : picked.filter((k) => k !== key);
	}

	function askBulk(effect: Effect | null, label: string) {
		bulk = { effect, label };
		bulkOpen = true;
	}

	const bulkLines = $derived(
		picked.map((k) => {
			const c = catalog.capabilities.find((x) => x.key === k);
			const before = effectAt(rules, k, node.scope);
			const after = bulk?.effect ?? null;
			const word = (e: Effect | null) =>
				e === 'allow'
					? 'Allow'
					: e === 'deny'
						? 'Deny'
						: mode === 'user'
							? 'Inherit'
							: 'No rule';
			return `${c?.label ?? k}: ${word(before)} → ${word(after)}`;
		})
	);

	function applyBulk() {
		let next = rules;
		for (const k of picked) next = setRule(next, k, node.scope, bulk?.effect ?? null);
		onchange(next);
		picked = [];
	}
</script>

{#snippet row(c: Capability)}
	<li class="row" class:high={c.risk === 'high'}>
		{#if !readonly}
			<Checkbox
				label="Select {c.label}"
				hideLabel
				checked={picked.includes(c.key)}
				onchange={(e) => togglePick(c.key, e.currentTarget.checked)}
			/>
		{/if}
		<div class="what">
			<span class="name">{c.label}</span>
			{#if c.risk === 'high'}<Badge tone="warn">High risk</Badge>{/if}
			<p class="desc">{c.description}</p>
		</div>
		<div class="control">
			{#if mode === 'token'}
				<Checkbox
					label="Grant {c.label}"
					hideLabel
					checked={value(c) === 'allow'}
					disabled={readonly}
					onchange={(e) => set(c, e.currentTarget.checked ? 'allow' : null)}
				/>
			{:else if mode === 'group'}
				<TriState
					label="{c.label} for {node.label}"
					variant="rule"
					value={value(c)}
					highRisk={c.risk === 'high'}
					disabled={readonly}
					onchange={(v) => set(c, v)}
				/>
			{:else}
				<TriState
					label="{c.label} for {node.label}"
					value={value(c)}
					inherited={inherited(c).effect}
					inheritedFrom={inheritedText(c)}
					highRisk={c.risk === 'high'}
					disabled={readonly}
					onchange={(v) => set(c, v)}
				/>
			{/if}
		</div>
	</li>
{/snippet}

<section class="matrix" aria-label="Actions for {node.label}">
	<header class="head">
		<h3>{node.label}{envLabel && node.scope.kind === 'resource' ? ` on ${envLabel}` : ''}</h3>
		<p class="muted">{scopeConsequence(node, envLabel)}</p>
	</header>
	<div class="tools">
		<TextField
			label="Filter actions"
			hideLabel
			placeholder="Filter actions"
			type="search"
			bind:value={filter}
		/>
		{#if !readonly && picked.length}
			<div class="bulk" role="group" aria-label="Change {picked.length} selected actions">
				<span class="muted">{picked.length} selected</span>
				<Button
					size="sm"
					onclick={() => askBulk('allow', mode === 'token' ? 'Grant' : 'Allow')}
					>{mode === 'token' ? 'Grant' : 'Allow'}</Button
				>
				{#if mode !== 'token'}<Button size="sm" onclick={() => askBulk('deny', 'Deny')}
						>Deny</Button
					>{/if}
				<Button
					size="sm"
					variant="ghost"
					onclick={() => askBulk(null, mode === 'user' ? 'Reset to inherit' : 'Clear')}
					>{mode === 'user' ? 'Reset to inherit' : 'Clear'}</Button
				>
			</div>
		{/if}
	</div>

	{#if groups.length === 0}
		<p class="muted">
			{filter
				? 'No action matches the filter.'
				: mode === 'token'
					? 'You hold no action you could grant here.'
					: 'No action can be granted at this scope.'}
		</p>
	{/if}
	{#each groups as g (g.type)}
		<div class="group">
			<h4>{g.label}</h4>
			{#if g.common.length}<ul role="list">
					{#each g.common as c (c.key)}{@render row(c)}{/each}
				</ul>{/if}
			{#if g.advanced.length}
				<Disclosure
					summary="{g.advanced.length} less common {g.advanced.length === 1
						? 'action'
						: 'actions'}"
					open={!!filter || g.advanced.some((c) => value(c) !== 'inherit')}
				>
					<ul role="list">
						{#each g.advanced as c (c.key)}{@render row(c)}{/each}
					</ul>
				</Disclosure>
			{/if}
		</div>
	{/each}
</section>

<ConfirmDialog
	bind:open={bulkOpen}
	title="{bulk?.label ?? 'Change'} {picked.length} {picked.length === 1 ? 'action' : 'actions'}?"
	message="For {node.label}. Nothing is saved until you save the permissions."
	consequences={bulkLines}
	confirmLabel="{bulk?.label ?? 'Change'} {picked.length} {picked.length === 1
		? 'action'
		: 'actions'}"
	onconfirm={applyBulk}
/>

<style>
	.matrix {
		display: grid;
		gap: var(--space-4);
		min-width: 0;
		align-content: start;
	}

	h3 {
		font-size: var(--text-section);
		line-height: var(--leading-section);
		color: var(--text-strong);
	}

	.head p {
		margin-top: 2px;
		max-width: 72ch;
	}

	.tools {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
	}

	.tools > :global(:first-child) {
		flex: 1 1 220px;
	}

	.bulk {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.group {
		display: grid;
		gap: var(--space-2);
	}

	h4 {
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
	}

	.row {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		align-items: start;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}

	.row + :global(.row) {
		margin-top: var(--space-2);
	}

	.row.high {
		border-left: 2px solid var(--warn-border);
	}

	.what {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-2);
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.desc {
		width: 100%;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	@media (max-width: 767px) {
		.row {
			grid-template-columns: auto minmax(0, 1fr);
		}

		.control {
			grid-column: 1 / -1;
		}
	}
</style>
