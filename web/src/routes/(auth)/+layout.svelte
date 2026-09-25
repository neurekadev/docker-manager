<script lang="ts">
	// Public pages (#16, #22): setup, sign-in, factor enrollment, invitation
	// and password reset share one quiet, centered layout: the logo lockup,
	// a panel, and the manager version.
	import { createQuery } from '@tanstack/svelte-query';
	import { healthQuery } from '$lib/api/queries';
	import Logo from '$lib/shell/Logo.svelte';

	let { children } = $props();
	const health = createQuery(() => healthQuery());
</script>

<div class="auth">
	<div class="column">
		<div class="brand"><Logo /></div>
		<main class="panel">
			{@render children()}
		</main>
		<p class="version num">
			{#if health.data}
				DockYard {health.data.version} ({health.data.commit.slice(0, 12)})
			{:else if health.fetchStatus === 'paused'}
				Waiting for the network…
			{:else if health.isError}
				Manager unreachable
			{/if}
		</p>
	</div>
</div>

<style>
	.auth {
		display: grid;
		place-items: start center;
		min-height: 100dvh;
		padding: clamp(24px, 10vh, 96px) var(--space-4) var(--space-8);
		background:
			radial-gradient(
				1200px 480px at 50% -10%,
				color-mix(in srgb, var(--accent) 9%, transparent),
				transparent 70%
			),
			var(--surface-canvas);
	}

	.column {
		display: flex;
		flex-direction: column;
		align-items: stretch;
		gap: var(--space-5);
		width: min(420px, 100%);
	}

	.brand {
		display: flex;
		justify-content: center;
	}

	.panel {
		padding: var(--space-6);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.version {
		min-height: 16px;
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-align: center;
	}

	@media (max-width: 480px) {
		.panel {
			padding: var(--space-5) var(--space-4);
		}
	}
</style>
