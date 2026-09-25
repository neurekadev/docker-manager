<script lang="ts">
	// Which connection does an image use? (#19 match preview): the
	// normalized reference, every matching connection best first, and the
	// selection with its reason. Deterministic for Docker Hub aliases, GHCR
	// and self-hosted registries; ties need an explicit choice.
	import { createQuery } from '@tanstack/svelte-query';
	import { registryMatchQuery, type RegistryMatch } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Card,
		ErrorState,
		Notice,
		Select,
		Skeleton,
		Table,
		TextField,
		type Column
	} from '$lib/ui';
	import { stackNamesQuery } from '$lib/features/registries/model';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	type Candidate = RegistryMatch['candidates'][number];

	usePage({
		title: 'Which connection?',
		crumbs: [{ label: 'Registries', href: routes.registries() }, { label: 'Which connection?' }]
	});

	const scope = useEnvironmentScope();
	const stacks = createQuery(() => stackNamesQuery());
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

	const columns: Column<Candidate>[] = [
		{ id: 'name', header: 'Connection', cell: nameCell, stack: 'title' },
		{ id: 'binding', header: 'Binding', cell: bindingCell },
		{ id: 'spec', header: 'Matcher', cell: specCell },
		{ id: 'priority', header: 'Priority', cell: prioCell, numeric: true, width: '90px' },
		{ id: 'state', header: 'Result', cell: stateCell, width: '140px', stack: 'status' }
	];
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

<Card title="Check an image reference">
	<div class="fields">
		<TextField
			label="Image"
			mono
			bind:value={reference}
			placeholder="acme/app:1.4 or ghcr.io/acme/app"
			autocomplete="off"
			spellcheck="false"
		/>
		<Select
			label="Environment"
			bind:value={env}
			options={[
				{ value: '', label: 'Any environment' },
				...(scope.envs.data ?? []).map((e) => ({ value: e.id, label: e.name }))
			]}
		/>
		<Select
			label="Stack"
			bind:value={stack}
			options={[
				{ value: '', label: 'No stack' },
				...(stacks.data ?? []).map((s) => ({ value: s.id, label: s.name }))
			]}
		/>
	</div>
</Card>

{#if debounced}
	{#if match.isError}
		<ErrorState error={match.error} title="The reference could not be matched." compact />
	{:else if !m}
		<Skeleton height="120px" radius="lg" />
	{:else}
		<Notice
			tone={m.selection === 'connection'
				? 'info'
				: m.selection === 'anonymous'
					? 'info'
					: m.selection === 'ambiguous'
						? 'warn'
						: 'danger'}
			title={m.selection === 'connection' && m.selected
				? `Uses ${m.selected.name}`
				: SELECTION[m.selection].title}
			live="status"
		>
			<span class="mono">{m.reference}</span> (registry <span class="mono">{m.host}</span>,
			repository
			<span class="mono">{m.repository}</span>). {SELECTION[m.selection].body}
		</Notice>
		{#if m.candidates.length}
			<Card
				title="Matching connections"
				subtitle="Best first: binding, then repository matcher, then priority."
				padding="none"
			>
				<Table
					label="Matching connections"
					rows={m.candidates}
					{columns}
					rowKey={(c) => c.connection.id}
				/>
			</Card>
		{/if}
	{/if}
{/if}

<style>
	.fields {
		display: grid;
		grid-template-columns: 2fr 1fr 1fr;
		gap: var(--space-4);
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
