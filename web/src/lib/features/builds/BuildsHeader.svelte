<script lang="ts">
	// Header and tabs of the builds section (#33): build history and saved
	// build definitions. Both tabs show the same actions: "Build Image"
	// (primary), "New Definition" (opens the definitions with the create
	// dialog) and the page's extra ones (the build cache prune).
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import Play from '@lucide/svelte/icons/play';
	import Plus from '@lucide/svelte/icons/plus';
	import { routes } from '$lib/routes';
	import { Button, PageHeader, TabNav } from '$lib/ui';

	interface Props {
		/** The caller may start builds in at least one environment. */
		canBuild: boolean;
		/** The caller may save build definitions in at least one environment. */
		canDefine?: boolean;
		/** Opens the create dialog on the definitions tab (default: links there). */
		onnewdefinition?: () => void;
		environmentId?: string;
		description: string;
		/** Secondary actions before the others (the build cache prune). */
		extra?: Snippet;
	}

	let {
		canBuild,
		canDefine = false,
		onnewdefinition,
		environmentId,
		description,
		extra
	}: Props = $props();
</script>

<PageHeader title="Builds" {description}>
	{#snippet actions()}
		{@render extra?.()}
		{#if canDefine}
			{#if onnewdefinition}
				<Button icon={Plus} onclick={onnewdefinition}>New Definition</Button>
			{:else}
				<Button icon={Plus} href={routes.buildDefinitionNew()}>New Definition</Button>
			{/if}
		{/if}
		{#if canBuild}
			<Button variant="primary" icon={Play} href={routes.newBuild(environmentId)}
				>Build Image</Button
			>
		{/if}
	{/snippet}
</PageHeader>
<TabNav
	label="Builds Sections"
	current={page.url.pathname}
	items={[
		{ href: routes.builds(), label: 'History' },
		{ href: routes.buildDefinitions(), label: 'Definitions' }
	]}
/>
