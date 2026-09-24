<script lang="ts">
	// Offline indicator (#11, #23). Unstyled on purpose: the visual treatment
	// comes from the design system (#22). The live region is always present
	// so screen readers announce changes.
	import WifiOff from '@lucide/svelte/icons/wifi-off';
	import ServerOff from '@lucide/svelte/icons/server-off';
	import { connectivity } from './connectivity.svelte';
</script>

<div
	role="status"
	aria-live="polite"
	aria-label="Connection status"
	data-testid="connection-status"
	data-state={connectivity.state}
>
	{#if connectivity.state === 'offline'}
		<WifiOff aria-hidden="true" />
		<span>
			Offline. DockYard cannot reach the network; live data is unavailable and no changes are
			sent.
		</span>
	{:else if connectivity.state === 'manager-unreachable'}
		<ServerOff aria-hidden="true" />
		<span>
			Manager unreachable. Live data is unavailable and no changes are sent until it responds.
		</span>
	{/if}
</div>
