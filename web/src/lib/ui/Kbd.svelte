<script lang="ts">
	// Keyboard shortcut hint (#22: "⌘K" in the search field).
	import type { Snippet } from 'svelte';
	let { children }: { children: Snippet } = $props();
</script>

<kbd>{@render children()}</kbd>

<style>
	kbd {
		display: inline-flex;
		align-items: center;
		height: 20px;
		padding: 0 6px;
		border: 1px solid var(--border-strong);
		border-radius: 4px;
		background: var(--surface-panel);
		color: var(--text-muted);
		font-family: var(--font-sans);
		font-size: 11px;
		font-weight: var(--weight-medium);
	}
</style>
