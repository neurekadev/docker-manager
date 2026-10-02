<script lang="ts">
	// Environment migration (#35): move every stack of the environment with
	// its data to another environment (EnvironmentMigrationWizard). With one
	// environment the wizard says that a second one is needed instead of
	// showing its steps; a running migration opens on its progress, the last
	// one on its result while it left something to do (the environment
	// page's "Review the Migration" leads here); an archived environment
	// runs no jobs.
	import { page } from '$app/state';
	import { createQuery } from '@tanstack/svelte-query';
	import { environmentQuery } from '$lib/api/queries';
	import Page from '$lib/features/common/Page.svelte';
	import { environmentIcon } from '$lib/features/common/resourceIcons';
	import EnvironmentMigrationWizard from '$lib/features/environments/EnvironmentMigrationWizard.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, Card, EmptyState, ErrorState, PageHeader, Skeleton } from '$lib/ui';

	const id = $derived(page.params.environmentId ?? '');
	const env = createQuery(() => environmentQuery(id));
	const e = $derived(env.data);

	usePage(() => ({
		title: `Migrate ${e?.name ?? 'Environment'}`,
		crumbs: [
			{ label: 'Environments', href: routes.environments() },
			{ label: e?.name ?? '…', href: routes.environment(id) },
			{ label: 'Migrate' }
		]
	}));
</script>

{#if env.isError}
	<ErrorState
		error={env.error}
		title="This environment could not be loaded."
		onretry={() => env.refetch()}
	/>
{:else if !e}
	<Page>
		<div aria-busy="true"><Skeleton height="320px" radius="lg" /></div>
	</Page>
{:else}
	<Page>
		<PageHeader
			title="Migrate {e.name}"
			description="Move its stacks and their data to another environment."
			{...environmentIcon(e.online)}
		/>
		<Card>
			{#if e.status === 'archived'}
				<EmptyState
					title="{e.name} is archived."
					description="Archived environments keep their stacks but run nothing. Re-attach it first."
					level={2}
					compact
				>
					{#snippet actions()}
						<Button href={routes.environment(e.id)}>Back to {e.name}</Button>
					{/snippet}
				</EmptyState>
			{:else}
				{#key e.id}<EnvironmentMigrationWizard environment={e} />{/key}
			{/if}
		</Card>
	</Page>
{/if}
