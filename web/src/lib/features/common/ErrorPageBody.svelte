<script lang="ts">
	// The body of the router error pages (#22): one heading that says what
	// happened, one sentence with what to do, and the ways on: Reload (not
	// for an unknown address), Back and Go to the dashboard. The root
	// +error.svelte shows it on its own; routes/(app)/+error.svelte inside
	// the app shell, so a signed-in user keeps the sidebar and top bar.
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import MapPinOff from '@lucide/svelte/icons/map-pin-off';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import { routes } from '$lib/routes';
	import { Button, IconTile } from '$lib/ui';

	let { status }: { status: number } = $props();

	const notFound = $derived(status === 404);
</script>

<svelte:head><title>{notFound ? 'Page Not Found' : 'Error'} · Docker Manager</title></svelte:head>

<section class="error-body" aria-labelledby="error-title">
	<IconTile
		icon={notFound ? MapPinOff : CircleAlert}
		color={notFound ? 'slate' : 'rose'}
		size="lg"
	/>
	<h1 id="error-title">{notFound ? 'Page Not Found' : 'This page failed to load'}</h1>
	<p class="desc">
		{notFound
			? 'Check the link, or go back.'
			: 'If it keeps happening, check the Docker Manager logs.'}
	</p>
	<div class="actions">
		{#if !notFound}
			<Button variant="primary" icon={RotateCw} onclick={() => location.reload()}
				>Reload</Button
			>
		{/if}
		<Button icon={ArrowLeft} onclick={() => history.back()}>Back</Button>
		<Button variant={notFound ? 'primary' : 'ghost'} href={routes.dashboard()}
			>Go to the Dashboard</Button
		>
	</div>
</section>

<style>
	.error-body {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-3);
		max-width: 480px;
		margin: 0 auto;
		padding: var(--space-12) var(--space-4);
		text-align: center;
	}

	h1 {
		margin-top: var(--space-1);
		font-size: var(--text-title-sm);
		line-height: var(--leading-title-sm);
	}

	.desc {
		color: var(--text-muted);
		font-size: var(--text-control);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: var(--space-2);
		margin-top: var(--space-2);
	}
</style>
