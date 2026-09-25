<script lang="ts">
	// Router errors (#22): unknown paths and unexpected rendering failures.
	import { page } from '$app/state';
	import Compass from '@lucide/svelte/icons/compass';
	import Logo from '$lib/shell/Logo.svelte';
	import { Button, EmptyState } from '$lib/ui';

	const notFound = $derived(page.status === 404);
</script>

<svelte:head><title>{notFound ? 'Page not found' : 'Error'} · DockYard</title></svelte:head>

<div class="error-page">
	<Logo />
	<h1>{notFound ? 'Page not found' : 'This page failed to load'}</h1>
	<EmptyState
		icon={Compass}
		color="slate"
		title={notFound
			? 'There is nothing at this address.'
			: 'DockYard hit an unexpected problem showing this page.'}
		description={notFound
			? 'Check the link, or go back to the dashboard.'
			: 'Reload the page. If it keeps happening, the manager logs have the details.'}
		level={2}
	>
		{#snippet actions()}
			<Button variant="primary" href="/">Go to the dashboard</Button>
		{/snippet}
	</EmptyState>
</div>

<style>
	.error-page {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-4);
		min-height: 100dvh;
		padding: var(--space-6);
	}

	h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
	}
</style>
