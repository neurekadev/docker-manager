<script lang="ts">
	// Archive (remove) an environment (#34): first the removal preview
	// (POST …/removal-previews) lists every dependent record kind and what
	// archiving does to it, and offers migrating stacks first (#35); then a
	// type-to-confirm DELETE with If-Match of the previewed revision. Nothing
	// on the host is touched.
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap, unwrapEmpty, type Environment, type Schema } from '$lib/api/client';
	import { liveKeys } from '$lib/live/keys';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import {
		Button,
		Dialog,
		DestructiveConfirm,
		ErrorState,
		Notice,
		Skeleton,
		toast,
		type AffectedResource
	} from '$lib/ui';
	import { dependentNoun, removalConsequences } from './model';

	let { env, open = $bindable(false) }: { env: Environment; open?: boolean } = $props();

	const qc = useQueryClient();
	let preview = $state<Schema<'EnvironmentRemovalPreview'> | null>(null);
	let loadError = $state<unknown>(null);
	let loading = $state(false);

	async function load() {
		loading = true;
		loadError = null;
		preview = null;
		try {
			preview = await unwrap(
				api.POST('/api/v1/environments/{environmentId}/removal-previews', {
					params: { path: { environmentId: env.id } }
				})
			);
		} catch (e) {
			loadError = e;
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		if (open) untrack(() => void load());
	});

	const affected = $derived<AffectedResource[]>(
		(preview?.dependents ?? []).flatMap((d) =>
			d.items.map((i) => ({ label: i.name, detail: dependentNoun(d.kind, 1) }))
		)
	);

	function migrateFirst() {
		open = false;
		environmentSelection.select(env.id);
		void goto(routes.stacks());
	}

	async function archive() {
		if (!preview) return;
		await unwrapEmpty(
			api.DELETE('/api/v1/environments/{environmentId}', {
				params: {
					path: { environmentId: env.id },
					header: { 'If-Match': `"${preview.revision}"` }
				}
			})
		);
		await Promise.all([
			qc.invalidateQueries({ queryKey: liveKeys.list('environments') }),
			qc.invalidateQueries({ queryKey: liveKeys.item('environments', env.id) }),
			qc.invalidateQueries({ queryKey: ['overview'] })
		]);
		if (environmentSelection.id === env.id) environmentSelection.select(null);
		toast.success(`Archived ${env.name}`, {
			body: 'Its stacks, history and backups are kept. Re-attach it from the archived environments.'
		});
		void goto(routes.environments());
	}
</script>

{#if open && !preview}
	<Dialog bind:open title="Archive {env.name}" size="sm">
		{#if loadError}
			<ErrorState
				error={loadError}
				title="The removal preview could not be loaded."
				onretry={load}
				compact
			/>
		{:else}
			<div aria-busy={loading}>
				<p class="muted">Checking what depends on {env.name}…</p>
				<Skeleton lines={3} height="16px" />
			</div>
		{/if}
	</Dialog>
{/if}

{#if preview}
	<DestructiveConfirm
		bind:open
		title="Archive {env.name}"
		consequences={removalConsequences(preview)}
		{affected}
		confirmText={env.name}
		confirmLabel="Archive environment"
		onconfirm={archive}
	>
		{#snippet extra()}
			{#if preview && preview.migration.stacks > 0}
				{@const n = preview.migration.stacks}
				<div class="offer">
					<Notice tone="info" title="Keep operating its stacks?" live="none">
						{n === 1 ? 'One stack runs' : `${n} stacks run`} on {env.name}. To keep
						operating {n === 1 ? 'it' : 'them'}, migrate {n === 1 ? 'it' : 'them'} with {n ===
						1
							? 'its'
							: 'their'} volumes to another environment first. Archived, {n === 1
							? 'it is'
							: 'they are'} kept but cannot be deployed until
						{env.name} is re-attached.
					</Notice>
					<Button size="sm" onclick={migrateFirst}
						>Migrate {n === 1 ? 'the stack' : `${n} stacks`} first</Button
					>
				</div>
			{/if}
		{/snippet}
	</DestructiveConfirm>
{/if}

<style>
	.offer {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: var(--space-2);
		margin-top: var(--space-4);
	}
</style>
