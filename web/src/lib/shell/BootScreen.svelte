<script lang="ts">
	// Boot screen (#22): shown while the session is being resolved, when the
	// app starts offline (the precached shell, #11: "Waiting for the
	// network…") or when the manager cannot be reached.
	import Spinner from '$lib/ui/Spinner.svelte';
	import ErrorState from '$lib/ui/ErrorState.svelte';
	import Logo from './Logo.svelte';

	interface Props {
		error?: unknown;
		waiting?: boolean;
		onretry?: () => void;
	}

	let { error = null, waiting = false, onretry }: Props = $props();
</script>

<div class="boot" aria-busy={!error}>
	<div class="brand">
		<Logo mark />
		<h1>Docker Manager</h1>
	</div>
	{#if error}
		<ErrorState {error} title="Docker Manager could not load your session." {onretry} compact />
	{:else if waiting}
		<p class="status" role="status">Waiting for the network…</p>
	{:else}
		<p class="status" role="status">
			<Spinner size={16} /> <span>Loading Docker Manager…</span>
		</p>
	{/if}
</div>

<style>
	.boot {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-5);
		min-height: 100dvh;
		padding: var(--space-6);
	}

	.brand {
		display: flex;
		align-items: center;
		gap: var(--space-1);
	}

	h1 {
		font-size: var(--text-title-sm);
		line-height: var(--leading-title-sm);
	}

	.status {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--text-muted);
	}
</style>
