<script lang="ts">
	// Offline indicator (#11, #23), styled by the design system (#22). The
	// live region is always present so screen readers announce changes;
	// the visible card appears only while offline or unreachable.
	import WifiOff from '@lucide/svelte/icons/wifi-off';
	import ServerOff from '@lucide/svelte/icons/server-off';
	import { connectivity } from './connectivity.svelte';
</script>

<div
	role="status"
	aria-live="polite"
	aria-label="Connection Status"
	data-testid="connection-status"
	data-state={connectivity.state}
	class="connection"
>
	{#if connectivity.state === 'offline'}
		<div class="card">
			<WifiOff size={18} strokeWidth={1.75} aria-hidden="true" />
			<span>
				Offline. Docker Manager cannot reach the network; live data is unavailable and no
				changes are sent.
			</span>
		</div>
	{:else if connectivity.state === 'manager-unreachable'}
		<div class="card">
			<ServerOff size={18} strokeWidth={1.75} aria-hidden="true" />
			<span>
				Manager unreachable. Live data is unavailable and no changes are sent until it
				responds.
			</span>
		</div>
	{/if}
</div>

<style>
	.card {
		display: flex;
		align-items: flex-start;
		gap: var(--space-3);
		max-width: 420px;
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
		color: var(--text-strong);
		pointer-events: auto;
	}

	.card :global(svg) {
		margin-top: 1px;
		color: var(--offline);
	}
</style>
