<script lang="ts">
	// The buttons at the end of a form or wizard step: right-aligned with
	// the primary action last; stacked full width (primary on top) below
	// 768 px, where touch targets grow.
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();
</script>

<div class="form-footer">{@render children()}</div>

<style>
	.form-footer {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		align-items: center;
		gap: var(--space-2);
		margin-top: var(--space-5);
	}

	@media (max-width: 767px) {
		.form-footer {
			flex-direction: column-reverse;
			align-items: stretch;
		}
	}
</style>
