<script lang="ts">
	// The buttons at the end of a form or wizard step: right-aligned with
	// the primary action last; stacked full width (primary on top) below
	// 768 px, where touch targets grow. `sticky`: a bar that stays at the
	// bottom of the window while a long form scrolls (the token grants),
	// with an optional status line (`summary`) at its start.
	import type { Snippet } from 'svelte';

	let {
		sticky = false,
		summary,
		children
	}: { sticky?: boolean; summary?: Snippet; children: Snippet } = $props();
</script>

<div class="form-footer" class:sticky>
	{#if summary}<div class="summary">{@render summary()}</div>{/if}
	{@render children()}
</div>

<style>
	.form-footer {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		align-items: center;
		gap: var(--space-2);
		margin-top: var(--space-5);
	}

	.summary {
		flex: 1 1 auto;
		min-width: 0;
		color: var(--text-muted);
	}

	.sticky {
		position: sticky;
		bottom: var(--space-4);
		z-index: var(--z-sticky);
		margin-top: 0;
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-lg);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
	}

	@media (max-width: 767px) {
		.form-footer {
			flex-direction: column-reverse;
			align-items: stretch;
		}

		.sticky {
			bottom: var(--space-2);
		}
	}
</style>
