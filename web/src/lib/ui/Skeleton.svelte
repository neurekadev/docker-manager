<script lang="ts">
	// Loading placeholder (#22). Decorative: the region that is loading sets
	// aria-busy="true" and, where useful, a visually hidden "Loading …" text.
	interface Props {
		width?: string;
		height?: string;
		/** Several text-like lines (the last one shorter). */
		lines?: number;
		radius?: 'sm' | 'md' | 'lg';
	}

	let { width = '100%', height = '14px', lines = 1, radius = 'sm' }: Props = $props();
</script>

{#if lines > 1}
	<span class="stack" aria-hidden="true">
		{#each Array.from({ length: lines }, (_, n) => n) as i (i)}
			<span
				class="sk"
				style="width: {i === lines - 1
					? '62%'
					: width}; height: {height}; border-radius: var(--radius-{radius});"
			></span>
		{/each}
	</span>
{:else}
	<span
		class="sk"
		aria-hidden="true"
		style="width: {width}; height: {height}; border-radius: var(--radius-{radius});"
	></span>
{/if}

<style>
	.stack {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		width: 100%;
	}

	.sk {
		display: block;
		background: linear-gradient(
			90deg,
			var(--surface-raised) 0%,
			var(--surface-hover) 50%,
			var(--surface-raised) 100%
		);
		background-size: 200% 100%;
		animation: shimmer 1.4s linear infinite;
	}

	@keyframes shimmer {
		from {
			background-position: 100% 0;
		}
		to {
			background-position: -100% 0;
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.sk {
			animation: none;
		}
	}
</style>
