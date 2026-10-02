<script lang="ts">
	// The actions of one scope (#17): plain-language capability names
	// grouped by resource type in collapsible sections (open by default
	// only where rules are set), high-risk ones marked once, less common
	// ones collapsed. Groups set Allow or Deny (no rule = deny); users
	// override with Inherit / Allow / Deny and see what they inherit; API
	// tokens (#31) grant a subset of what the caller holds. Groups and
	// tokens can start from a preset (Viewer, Operator, Admin), and every
	// section can allow or clear all of its actions at once. All of it only
	// changes the draft: the page saves, after a preview.
	import { untrack } from 'svelte';
	import { SvelteMap } from 'svelte/reactivity';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import { Badge, Button, Checkbox, Select, TextField, TriState, type TriValue } from '$lib/ui';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import {
		capabilitiesAt,
		effectAt,
		groupCapabilities,
		inheritedDecision,
		scopeConsequence,
		setRule,
		type Capability,
		type CapabilityGroup,
		type Catalog,
		type Rule,
		type ScopeNode
	} from './permissions';
	import {
		PRESETS,
		applyPreset,
		matchingPreset,
		sectionCounts,
		setMany,
		type PresetId
	} from './presets';

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

	const available = $derived(
		capabilitiesAt(catalog, node).filter((c) => mode !== 'token' || !held || held.has(c.key))
	);
	const groups = $derived(groupCapabilities(catalog, available, filter));
	const envId = $derived(node.environmentId ?? node.scope.environmentId);
	const envLabel = $derived(envId ? environmentName?.(envId) : undefined);
	const preset = $derived(matchingPreset(rules, node.scope, available));
	const presetInfo = $derived(PRESETS.find((p) => p.id === preset));

	// Sections open by default where the scope has rules when it is shown;
	// the user's own toggles win until another scope is chosen.
	const toggled = new SvelteMap<string, boolean>();
	const openAtFirst = $derived.by(() => {
		const scope = node.scope;
		return untrack(
			() =>
				new Set(
					groupCapabilities(catalog, available)
						.filter((g) => {
							const n = sectionCounts(rules, scope, [...g.common, ...g.advanced]);
							return n.allow + n.deny > 0;
						})
						.map((g) => g.type)
				)
		);
	});
	$effect.pre(() => {
		void node.key;
		untrack(() => toggled.clear());
	});
	const isOpen = (type: string) => !!filter || (toggled.get(type) ?? openAtFirst.has(type));

	const allowWord = $derived(mode === 'token' ? 'Grant' : 'Allow');
	const clearWord = $derived(mode === 'user' ? 'Inherit All' : 'Clear');

	function value(c: Capability): TriValue {
		return effectAt(rules, c.key, node.scope) ?? 'inherit';
	}

	function set(c: Capability, v: TriValue | null) {
		onchange(setRule(rules, c.key, node.scope, v === 'inherit' || v === null ? null : v));
	}

	function inherited(c: Capability) {
		return inheritedDecision(groupRules, c.key, node.scope, node.environmentId);
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

	const sectionActions = (g: CapabilityGroup) => [...g.common, ...g.advanced];

	function summary(g: CapabilityGroup): string {
		const all = sectionActions(g);
		const n = sectionCounts(rules, node.scope, all);
		const parts: string[] = [];
		if (n.allow) parts.push(`${n.allow} ${mode === 'token' ? 'granted' : 'allowed'}`);
		if (n.deny) parts.push(`${n.deny} denied`);
		const of = `${all.length} ${all.length === 1 ? 'action' : 'actions'}`;
		return parts.length ? `${parts.join(', ')} of ${of}` : of;
	}

	function startFrom(id: string) {
		if (!PRESETS.some((p) => p.id === id)) return;
		onchange(applyPreset(rules, id as PresetId, node.scope, available));
	}
</script>

{#snippet row(c: Capability)}
	<li class="row">
		<div class="what">
			<span class="name">{c.label}</span>
			{#if c.risk === 'high'}<Badge tone="warn">High Risk</Badge>{/if}
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
					disabled={readonly}
					onchange={(v) => set(c, v)}
				/>
			{:else}
				<TriState
					label="{c.label} for {node.label}"
					value={value(c)}
					inherited={inherited(c).effect}
					inheritedFrom={inheritedText(c)}
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
		{#if mode !== 'user' && !readonly && available.length}
			<div class="preset">
				<Select
					label="Start From"
					placeholder="Choose a starting point"
					options={[
						...PRESETS.map((p) => ({ value: p.id, label: p.label })),
						{ value: 'custom', label: 'Custom', disabled: true }
					]}
					value={preset ?? ''}
					description={presetInfo
						? presetInfo.description
						: 'Sets every action below at once. Nothing is saved until you save.'}
					onchange={startFrom}
				/>
			</div>
		{/if}
		<div class="filter">
			<TextField
				label="Filter Actions"
				hideLabel
				placeholder="Filter actions"
				type="search"
				bind:value={filter}
			/>
		</div>
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
	<div class="sections">
		{#each groups as g (g.type)}
			{@const open = isOpen(g.type)}
			<div class="section">
				<div class="section-head">
					<h4>
						<button
							type="button"
							class="toggle"
							aria-expanded={open}
							onclick={() => toggled.set(g.type, !open)}
						>
							<ChevronRight size={14} aria-hidden="true" class={open ? 'open' : ''} />
							<span class="label">{g.label}</span>
							<span class="count">{summary(g)}</span>
						</button>
					</h4>
					{#if !readonly}
						<div class="section-actions">
							<Button
								size="sm"
								variant="ghost"
								aria-label="{allowWord} All {g.label}"
								onclick={() =>
									onchange(
										setMany(rules, sectionActions(g), node.scope, 'allow')
									)}>{allowWord} All</Button
							>
							<Button
								size="sm"
								variant="ghost"
								aria-label="{clearWord} {g.label}"
								onclick={() =>
									onchange(setMany(rules, sectionActions(g), node.scope, null))}
								>{clearWord}</Button
							>
						</div>
					{/if}
				</div>
				{#if open}
					{#if g.common.length}<ul role="list" class="rows">
							{#each g.common as c (c.key)}{@render row(c)}{/each}
						</ul>{/if}
					{#if g.advanced.length}
						<div class="advanced">
							<Disclosure
								summary="{g.advanced.length} less common {g.advanced.length === 1
									? 'action'
									: 'actions'}"
								open={!!filter || g.advanced.some((c) => value(c) !== 'inherit')}
							>
								<ul role="list" class="rows">
									{#each g.advanced as c (c.key)}{@render row(c)}{/each}
								</ul>
							</Disclosure>
						</div>
					{/if}
				{/if}
			</div>
		{/each}
	</div>
</section>

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
		align-items: flex-end;
		gap: var(--space-3);
	}

	.preset {
		flex: 1 1 280px;
		max-width: 420px;
	}

	.filter {
		flex: 1 1 220px;
	}

	.sections {
		display: grid;
		border-top: 1px solid var(--border-subtle);
	}

	.section {
		border-bottom: 1px solid var(--border-subtle);
	}

	.section-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-1) var(--space-2);
		padding: var(--space-1) 0;
	}

	h4 {
		flex: 1 1 220px;
		min-width: 0;
		font-size: var(--text-subsection);
	}

	.toggle {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		width: 100%;
		min-height: var(--control-height);
		padding: 0 var(--space-1);
		border: 0;
		border-radius: var(--radius-md);
		background: transparent;
		color: var(--text-strong);
		font: inherit;
		font-weight: var(--weight-semibold);
		text-align: left;
		cursor: pointer;
	}

	.toggle:hover {
		background: var(--surface-hover);
	}

	.toggle :global(svg) {
		flex: none;
		color: var(--text-muted);
		transition: transform var(--duration-fast) var(--ease-out);
	}

	.toggle :global(svg.open) {
		transform: rotate(90deg);
	}

	.count {
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-regular);
	}

	.section-actions {
		display: flex;
		gap: var(--space-1);
	}

	.rows {
		display: grid;
		margin-bottom: var(--space-2);
	}

	.advanced {
		padding: 0 0 var(--space-3) var(--space-1);
	}

	/* One flat list per section (no card inside the card), the controls
	   right-aligned in one column. */
	.row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: start;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-1) var(--space-2) var(--space-6);
		border-top: 1px solid var(--border-subtle);
	}

	.row:first-child {
		border-top: 0;
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

	.control {
		justify-self: end;
		max-width: 300px;
		text-align: right;
	}

	.control :global(.tri) {
		align-items: flex-end;
	}

	@media (max-width: 767px) {
		.row {
			grid-template-columns: minmax(0, 1fr);
			gap: var(--space-2);
			padding-left: 0;
			padding-right: 0;
		}

		.control {
			justify-self: start;
			max-width: none;
			text-align: left;
		}

		.control :global(.tri) {
			align-items: flex-start;
		}

		.advanced {
			padding-left: 0;
		}
	}
</style>
