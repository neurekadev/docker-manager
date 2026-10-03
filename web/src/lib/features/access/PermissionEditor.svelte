<script lang="ts">
	// The permission editor (#17, #22 "PermissionTree"): a searchable
	// resource tree beside the actions of the chosen scope. Used for group
	// rules, user overrides and API token grants (#31). Edits stay local
	// until the page saves them.
	import { createQuery } from '@tanstack/svelte-query';
	import { environmentsQuery } from '$lib/api/queries';
	import { environmentName } from '$lib/features/common/data';
	import ActionMatrix from './ActionMatrix.svelte';
	import ResourceTree from './ResourceTree.svelte';
	import { instanceNode } from './tree';
	import type { Catalog, InheritedGroup, Rule, ScopeNode } from './permissions';

	type Props = {
		catalog: Catalog;
		mode: 'group' | 'user' | 'token';
		rules: Rule[];
		/** User mode: the user's groups, highest priority first. */
		groups?: InheritedGroup[];
		held?: ReadonlySet<string>;
		readonly?: boolean;
		onchange: (rules: Rule[]) => void;
	};

	let { catalog, mode, rules, groups, held, readonly = false, onchange }: Props = $props();

	const envs = createQuery(() => environmentsQuery());
	let selected = $state<ScopeNode>(instanceNode());
</script>

<div class="editor">
	<aside class="tree-pane">
		<ResourceTree {rules} selectedKey={selected.key} onselect={(n) => (selected = n)} />
	</aside>
	<div class="actions-pane">
		<ActionMatrix
			{catalog}
			node={selected}
			{mode}
			{rules}
			{groups}
			{held}
			{readonly}
			environmentName={(id) => environmentName(envs.data, id)}
			{onchange}
		/>
	</div>
</div>

<style>
	.editor {
		display: grid;
		grid-template-columns: minmax(260px, 320px) minmax(0, 1fr);
		gap: var(--space-5);
		align-items: start;
	}

	.tree-pane {
		position: sticky;
		top: calc(var(--topbar-height) + var(--space-4));
		padding-right: var(--space-4);
		border-right: 1px solid var(--border-subtle);
		min-width: 0;
	}

	@media (max-width: 1023px) {
		.editor {
			grid-template-columns: minmax(0, 1fr);
		}

		.tree-pane {
			position: static;
			padding: 0 0 var(--space-4);
			border-right: 0;
			border-bottom: 1px solid var(--border-subtle);
		}

		.tree-pane :global(nav) {
			max-height: 320px;
		}
	}
</style>
