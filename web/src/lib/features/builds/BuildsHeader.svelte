<script lang="ts">
	// Header and tabs of the builds section (#33): build history and saved
	// build definitions, with "Build image" as the primary action.
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import Play from '@lucide/svelte/icons/play';
	import { routes } from '$lib/routes';
	import { Button, PageHeader, TabNav } from '$lib/ui';

	interface Props {
		/** The caller may start builds in at least one environment. */
		canBuild: boolean;
		environmentId?: string;
		description: string;
		/** Secondary actions before "Build image" (the history's build cache prune). */
		extra?: Snippet;
	}

	let { canBuild, environmentId, description, extra }: Props = $props();
</script>

<PageHeader title="Builds" {description}>
	{#snippet actions()}
		{@render extra?.()}
		{#if canBuild}
			<Button variant="primary" icon={Play} href={routes.newBuild(environmentId)}
				>Build image</Button
			>
		{/if}
	{/snippet}
</PageHeader>
<TabNav
	label="Builds sections"
	current={page.url.pathname}
	items={[
		{ href: routes.builds(), label: 'History' },
		{ href: routes.buildDefinitions(), label: 'Definitions' }
	]}
/>
