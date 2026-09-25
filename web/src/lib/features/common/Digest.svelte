<script lang="ts">
	// An image digest (#20): the first 12 hex digits in mono, the full value
	// on hover and for screen readers, with a copy button. `tone` marks a
	// candidate (accent) or a quarantined digest (danger).
	import CopyButton from '$lib/ui/CopyButton.svelte';
	import { shortDigest } from './data';

	let {
		value,
		tone = 'default',
		copy = true
	}: { value?: string | null; tone?: 'default' | 'accent' | 'danger'; copy?: boolean } = $props();
</script>

{#if value}
	<span class="digest {tone}">
		<span class="mono" title={value}>
			<span class="sr-only">{value}</span><span aria-hidden="true">{shortDigest(value)}</span>
		</span>
		{#if copy}<CopyButton {value} what="digest" size="sm" />{/if}
	</span>
{:else}
	<span class="muted">—</span>
{/if}

<style>
	.digest {
		display: inline-flex;
		align-items: center;
		gap: 2px;
		white-space: nowrap;
	}

	.accent .mono {
		color: var(--accent-text);
	}

	.danger .mono {
		color: var(--danger);
	}
</style>
