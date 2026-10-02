<script lang="ts">
	// The environment page's notice while stacks that moved away from it
	// (#35, an environment migration) left their stopped old copies here:
	// "3 stacks moved to NAS. Their old copies are still on this server."
	// with "Review the Migration" (the migrate page opens on the run's
	// result, where the copies are removed). The list of migrations answers
	// only callers who may migrate one of their stacks and lists only the
	// stacks the caller can see; anything else shows nothing. Keyed under
	// the jobs list, so a removal ending hides it without a reload. It shows
	// once the destinations are named.
	import { createQuery } from '@tanstack/svelte-query';
	import Truck from '@lucide/svelte/icons/truck';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { Button, Notice } from '$lib/ui';
	import { oldCopiesNotice } from './environment-migration';
	import { environmentMigrationsQuery } from './queries';

	interface Props {
		/** The environment the stacks moved away from. */
		environmentId: string;
		/** Whether to ask at all (the caller may migrate stacks somewhere). */
		enabled?: boolean;
	}

	let { environmentId, enabled = true }: Props = $props();

	const recent = createQuery(() => ({
		...environmentMigrationsQuery(environmentId),
		enabled,
		retry: false
	}));
	const envs = createQuery(() => ({
		...environmentsQuery(),
		enabled: enabled && !!recent.data
	}));
	const envName = (id: string) =>
		envs.data?.find((e) => e.id === id)?.name ?? 'another environment';
	const notice = $derived(recent.data ? oldCopiesNotice(recent.data, envName) : null);
</script>

{#if notice && !envs.isPending}
	<Notice tone="info" icon={Truck} title={notice.title} live="none">
		{notice.body}
		{#snippet actions()}
			<Button size="sm" href={routes.environmentMigrate(environmentId)}
				>Review the Migration</Button
			>
		{/snippet}
	</Notice>
{/if}
