<script lang="ts">
	// API tokens (#31): name, status, grants, expiry and last use (time and
	// client address). Values are never listed. Revoking stops a token and
	// its open streams at once.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap, unwrapEmpty } from '$lib/api/client';
	import {
		Badge,
		Button,
		ConfirmDialog,
		Dialog,
		Notice,
		Table,
		TextField,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { REVOKED_REASON, tokenStatus } from '$lib/features/access/model';
	import { capabilityLabel } from '$lib/features/access/permissions';
	import { accessKeys, catalogQuery, type APIToken } from '$lib/features/access/queries';

	interface Props {
		tokens: APIToken[];
		label: string;
		/** Owner view: all users' tokens (revoke through /api-tokens). */
		all?: boolean;
	}

	let { tokens, label, all = false }: Props = $props();
	const qc = useQueryClient();
	const catalog = createQuery(() => catalogQuery());

	let revoking = $state<APIToken | null>(null);
	let revokeOpen = $state(false);
	let renaming = $state<APIToken | null>(null);
	let newName = $state('');
	let renameError = $state<string | null>(null);

	async function revoke(t: APIToken) {
		try {
			if (all)
				await unwrapEmpty(
					api.DELETE('/api/v1/api-tokens/{tokenId}', {
						params: { path: { tokenId: t.id } }
					})
				);
			else
				await unwrapEmpty(
					api.DELETE('/api/v1/me/api-tokens/{tokenId}', {
						params: { path: { tokenId: t.id } }
					})
				);
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
		}
		toast.success(`Revoked the API token ${t.name}`);
		await qc.invalidateQueries({ queryKey: ['permissions'] });
	}

	async function rename(t: APIToken) {
		renameError = null;
		try {
			await unwrap(
				api.PATCH('/api/v1/me/api-tokens/{tokenId}', {
					params: { path: { tokenId: t.id } },
					body: { name: newName.trim() }
				})
			);
			toast.success(`Renamed the API token to ${newName.trim()}`);
			renaming = null;
			await qc.invalidateQueries({ queryKey: accessKeys.myTokens() });
		} catch (e) {
			renameError = actionError(e);
		}
	}

	function grantsText(t: APIToken): string {
		const labels = [
			...new Set(t.scopes.map((s) => capabilityLabel(catalog.data, s.capability)))
		];
		if (labels.length <= 3) return labels.join(', ') || 'No grants';
		return `${labels.slice(0, 3).join(', ')} and ${labels.length - 3} more`;
	}

	const columns = $derived<Column<APIToken>[]>([
		{ id: 'name', header: 'Token', cell: nameCell, sortValue: (t) => t.name, stack: 'title' },
		{ id: 'status', header: 'Status', cell: statusCell, width: '130px', stack: 'status' },
		...(all
			? [
					{
						id: 'user',
						header: 'User',
						cell: userCell,
						width: '140px',
						sortValue: (t: APIToken) => t.username ?? ''
					}
				]
			: []),
		{
			id: 'expires',
			header: 'Expires',
			cell: expiresCell,
			width: '150px',
			sortValue: (t) => t.expiresAt ?? '9'
		},
		{
			id: 'used',
			header: 'Last used',
			cell: usedCell,
			width: '170px',
			sortValue: (t) => t.lastUsedAt ?? ''
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '170px',
			stack: 'actions'
		}
	]);
</script>

{#snippet nameCell(t: APIToken)}<NameCell name={t.name} sub={grantsText(t)} />{/snippet}
{#snippet statusCell(t: APIToken)}
	{@const s = tokenStatus(t.status)}
	<span title={t.revokedReason ? REVOKED_REASON[t.revokedReason] : undefined}
		><Badge tone={s.tone} dot>{s.label}</Badge></span
	>
{/snippet}
{#snippet userCell(t: APIToken)}{t.username ?? t.userId}{/snippet}
{#snippet expiresCell(t: APIToken)}
	{#if t.expiresAt}<span class="num" title={formatDateTime(t.expiresAt)}
			>{formatRelative(t.expiresAt)}</span
		>{:else}<Badge tone="warn">Never</Badge>{/if}
{/snippet}
{#snippet usedCell(t: APIToken)}
	{#if t.lastUsedAt}
		<NameCell name={formatRelative(t.lastUsedAt)} sub={t.lastUsedIp} subMono />
	{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet actionsCell(t: APIToken)}
	{#if t.status === 'active'}
		<span class="acts">
			{#if !all}
				<Button
					size="sm"
					variant="ghost"
					onclick={() => {
						renaming = t;
						newName = t.name;
						renameError = null;
					}}>Rename</Button
				>
			{/if}
			<Button
				size="sm"
				variant="danger-soft"
				onclick={() => {
					revoking = t;
					revokeOpen = true;
				}}>Revoke</Button
			>
		</span>
	{/if}
{/snippet}

<Table
	{label}
	rows={tokens}
	{columns}
	rowKey={(t) => t.id}
	sort={{ column: 'name', direction: 'asc' }}
/>

<ConfirmDialog
	bind:open={revokeOpen}
	title="Revoke {revoking?.name ?? 'this token'}?"
	consequences={[
		'Scripts using it get 401 from now on, and its open streams close.',
		'Revoking is final: create a new token to replace it.'
	]}
	confirmLabel="Revoke token"
	tone="danger"
	onconfirm={() => (revoking ? revoke(revoking) : undefined)}
/>
<Dialog
	open={!!renaming}
	title="Rename {renaming?.name ?? 'token'}"
	size="sm"
	onclose={() => (renaming = null)}
>
	<TextField label="Name" bind:value={newName} required />
	{#if renameError}<Notice tone="danger" title="Not renamed" live="alert">{renameError}</Notice
		>{/if}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (renaming = null)}>Cancel</Button>
		<Button
			variant="primary"
			disabled={!newName.trim()}
			onclick={() => renaming && rename(renaming)}>Rename token</Button
		>
	{/snippet}
</Dialog>

<style>
	.acts {
		display: inline-flex;
		gap: var(--space-2);
	}
</style>
