<script lang="ts">
	// Public content pages (template registry): readable without signing in,
	// with the logo and a sign-in link instead of the app shell.
	import { routes } from '$lib/routes';
	import Logo from '$lib/shell/Logo.svelte';
	import { Button } from '$lib/ui';

	let { children } = $props();
</script>

<div class="public">
	<header class="bar">
		<Logo href={routes.registry()} />
		<Button variant="ghost" href={routes.dashboard()}>Sign In</Button>
	</header>
	<main class="content">
		{@render children()}
	</main>
</div>

<style>
	.public {
		min-height: 100dvh;
		background: var(--surface-canvas);
	}

	.bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: var(--space-3) var(--space-5);
		border-bottom: 1px solid var(--border-subtle);
		background: var(--surface-shell);
	}

	.content {
		max-width: 1200px;
		margin: 0 auto;
		padding: var(--space-5);
	}
</style>
