<script lang="ts">
	// Test an image (#19 match preview): which registry connection pulls,
	// deploys and update checks use for an image reference, in an
	// environment or a stack: every matching connection best first, and the
	// selection with its reason. Deterministic for Docker Hub aliases, GHCR
	// and self-hosted registries; ties need an explicit choice.
	import { createQuery } from '@tanstack/svelte-query';
	import { registryMatchQuery, type RegistryMatch } from '$lib/api/queries';
	import {
		Badge,
		Button,
		Dialog,
		ErrorState,
		Notice,
		Select,
		Skeleton,
		Table,
		TextField,
		type Column
	} from '$lib/ui';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';
	import { stackNamesQuery } from './model';

	type Candidate = RegistryMatch['candidates'][number];

	let { open = $bindable(false) }: { open?: boolean } = $props();

	const scope = useEnvironmentScope();
	const stacks = createQuery(() => ({ ...stackNamesQuery(), enabled: open }));
	let reference = $state('');
	let env = $state('');
	let stack = $state('');
	let debounced = $state('');
	$effect(() => {
		const r = reference.trim();
		const t = setTimeout(() => (debounced = r), 400);
		return () => clearTimeout(t);
	});
	const match = createQuery(() =>
		registryMatchQuery(debounced, {
			environmentId: env || undefined,
			stackId: stack || undefined
		})
	);
	const m = $derived(match.data);
	const samePriority = $derived(
		new Set((m?.candidates ?? []).map((c) => c.connection.priority ?? 0)).size <= 1
	);

	const SELECTION: Record<string, { title: string; body: string }> = {
		connection: { title: '', body: 'Pulls, deploys and update checks use this connection.' },
		anonymous: {
			title: 'No connection matches',
			body: 'Pulls and checks are anonymous, so only public images work.'
		},
		ambiguous: {
			title: 'Several connections match equally well',
			body: 'A pull must name one explicitly. Make one more specific or change a priority.'
		},
		revoked: {
			title: 'The best match is revoked',
			body: 'Jobs fail instead of falling back to anonymous access. Rotate its credential or remove it.'
		}
	};

	const columns: Column<Candidate>[] = $derived([
		{ id: 'name', header: 'Connection', cell: nameCell, stack: 'title' },
		{ id: 'binding', header: 'Used for', cell: bindingCell, stack: 'meta' },
		{ id: 'spec', header: 'Repositories', cell: specCell, stack: 'meta' },
		...(samePriority
			? []
			: [
					{
						id: 'priority',
						header: 'Priority',
						cell: prioCell,
						numeric: true,
						width: '90px',
						stack: 'meta'
					} satisfies Column<Candidate>
				]),
		{ id: 'state', header: 'Result', cell: stateCell, width: '160px', stack: 'status' }
	]);
</script>

{#snippet nameCell(c: Candidate)}<span class="name">{c.connection.name}</span>{/snippet}
{#snippet bindingCell(c: Candidate)}
	{c.binding === 'stack'
		? 'This stack'
		: c.binding === 'environment'
			? 'This environment'
			: 'Everywhere'}
{/snippet}
{#snippet specCell(c: Candidate)}<span class="mono">{c.connection.repositoryPattern || '*'}</span
	>{/snippet}
{#snippet prioCell(c: Candidate)}<span class="num">{c.connection.priority ?? 0}</span>{/snippet}
{#snippet stateCell(c: Candidate)}
	{#if m?.selected?.id === c.connection.id}
		<Badge tone={c.connection.status === 'revoked' ? 'danger' : 'ok'} dot
			>{c.connection.status === 'revoked' ? 'Selected, revoked' : 'Selected'}</Badge
		>
	{:else if m?.tied?.includes(c.connection.id)}<Badge tone="warn" dot>Tied</Badge>
	{:else}<span class="muted">Less specific</span>{/if}
{/snippet}

<Dialog
	bind:open
	title="Test an image"
	description="See which connection Docker Manager uses to pull an image."
	size="lg"
>
	<div class="body">
		<div class="fields" class:two={scope.single}>
			<TextField
				label="Image"
				mono
				bind:value={reference}
				placeholder="acme/app:1.4 or ghcr.io/acme/app"
				autocomplete="off"
				spellcheck="false"
			/>
			{#if !scope.single}
				<Select
					label="Environment"
					bind:value={env}
					options={[
						{ value: '', label: 'Any environment' },
						...(scope.envs.data ?? []).map((e) => ({ value: e.id, label: e.name }))
					]}
				/>
			{/if}
			<Select
				label="Stack"
				bind:value={stack}
				options={[
					{ value: '', label: 'No stack' },
					...(stacks.data ?? []).map((s) => ({ value: s.id, label: s.name }))
				]}
			/>
		</div>

		{#if debounced}
			{#if match.isError}
				<ErrorState
					error={match.error}
					title="The image could not be matched."
					bare
					compact
				/>
			{:else if !m}
				<Skeleton height="120px" radius="lg" />
			{:else}
				<Notice
					tone={m.selection === 'ambiguous'
						? 'warn'
						: m.selection === 'revoked'
							? 'danger'
							: 'info'}
					title={m.selection === 'connection' && m.selected
						? `Uses ${m.selected.name}`
						: SELECTION[m.selection].title}
					live="status"
				>
					<span class="mono">{m.reference}</span>
					(registry <span class="mono">{m.host}</span>, repository
					<span class="mono">{m.repository}</span>). {SELECTION[m.selection].body}
				</Notice>
				{#if m.candidates.length}
					<Table
						label="Matching connections, best first"
						rows={m.candidates}
						{columns}
						rowKey={(c) => c.connection.id}
					/>
				{/if}
			{/if}
		{/if}
	</div>
	{#snippet footer()}
		<Button variant="secondary" onclick={() => (open = false)}>Close</Button>
	{/snippet}
</Dialog>

<style>
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.fields {
		display: grid;
		grid-template-columns: 2fr 1fr 1fr;
		gap: var(--space-4);
	}

	/* One environment: no environment picker. */
	.two {
		grid-template-columns: 2fr 1fr;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	@media (max-width: 767px) {
		.fields {
			grid-template-columns: 1fr;
		}
	}
</style>
