<script lang="ts">
	// Effective access (#17): for every capability and scope named by the
	// user's overrides or group rules, the decision and why (the deciding
	// rule: user override, group rule or default deny). Anything not listed
	// is denied.
	import { createQuery } from '@tanstack/svelte-query';
	import { environmentsQuery } from '$lib/api/queries';
	import { Badge, Table, type Column } from '$lib/ui';
	import { environmentName } from '$lib/features/common/data';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { capabilityLabel, scopeLabel, type Catalog, type Effective } from './permissions';

	interface Props {
		entries: Effective[];
		catalog?: Catalog;
		label: string;
		/** Keys (capability|scope) that differ from the saved state. */
		changed?: ReadonlySet<string>;
	}

	let { entries, catalog, label, changed }: Props = $props();
	const envs = createQuery(() => environmentsQuery());

	const SOURCE: Record<string, string> = {
		owner: 'Owner',
		user_rule: 'User Override',
		group_rule: 'Group Rule',
		default_deny: 'No Rule',
		token_scope: 'Token Scope',
		unknown_capability: 'Unknown Action',
		owner_only: 'Owner Only',
		inactive_account: 'Account Disabled'
	};

	type Row = Effective & { key: string };
	const rows = $derived<Row[]>(
		entries.map((e) => ({ ...e, key: `${e.capability}|${JSON.stringify(e.scope)}` }))
	);

	const columns: Column<Row>[] = [
		{
			id: 'action',
			header: 'Action',
			cell: actionCell,
			sortValue: (r) => capabilityLabel(catalog, r.capability),
			stack: 'title'
		},
		{ id: 'decision', header: 'Decision', cell: decisionCell, width: '120px', stack: 'status' },
		{ id: 'source', header: 'Decided By', cell: sourceCell, width: '140px' },
		{ id: 'reason', header: 'Why', cell: reasonCell }
	];
</script>

{#snippet actionCell(r: Row)}
	<NameCell
		name={capabilityLabel(catalog, r.capability)}
		sub="On {scopeLabel(r.scope, { environment: (id) => environmentName(envs.data, id) })}"
	/>
{/snippet}
{#snippet decisionCell(r: Row)}
	{#if r.allowed}<Badge tone="ok" dot>Allowed</Badge>{:else}<Badge tone="danger" dot>Denied</Badge
		>{/if}
{/snippet}
{#snippet sourceCell(r: Row)}{SOURCE[r.source] ?? r.source}{/snippet}
{#snippet reasonCell(r: Row)}<span class="muted small">{r.reason}</span>{/snippet}

<Table
	{label}
	{rows}
	{columns}
	rowKey={(r) => r.key}
	{changed}
	sort={{ column: 'action', direction: 'asc' }}
/>

<style>
	.small {
		font-size: var(--text-caption);
	}
</style>
