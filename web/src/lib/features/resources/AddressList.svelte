<script lang="ts">
	// Container addresses in a table cell (#6, #7): the first address, "+N"
	// for the others, every "network: address" in the title and for screen
	// readers; a muted dash without any (stopped, host networking).
	import type { ContainerAddress } from './model';

	interface Props {
		addresses: ContainerAddress[];
	}

	let { addresses }: Props = $props();
	const first = $derived(addresses[0]);
	const all = $derived(addresses.map((a) => `${a.network}: ${a.address}`).join('\n'));
</script>

{#if first}
	<span class="addresses" title={all}>
		<span class="mono">{first.address}</span>{#if addresses.length > 1}<span
				class="muted"
				aria-hidden="true"
			>
				+{addresses.length - 1}</span
			><span class="sr-only"
				>, {addresses
					.slice(1)
					.map((a) => a.address)
					.join(', ')}</span
			>{/if}
	</span>
{:else}<span class="muted">—</span>{/if}

<style>
	.addresses {
		white-space: nowrap;
		font-size: var(--text-caption);
	}
</style>
