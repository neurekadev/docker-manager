<script lang="ts">
	// Indeterminate progress for buttons and inline loading. Decorative: the
	// owner sets aria-busy or a live region; the spinner itself is hidden.
	let { size = 16 }: { size?: number } = $props();
</script>

<svg
	class="spinner"
	width={size}
	height={size}
	viewBox="0 0 16 16"
	fill="none"
	aria-hidden="true"
	data-testid="spinner"
>
	<circle
		cx="8"
		cy="8"
		r="6.25"
		stroke="currentColor"
		stroke-opacity="0.25"
		stroke-width="1.75"
	/>
	<path
		d="M14.25 8A6.25 6.25 0 0 0 8 1.75"
		stroke="currentColor"
		stroke-width="1.75"
		stroke-linecap="round"
	/>
</svg>

<style>
	.spinner {
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.spinner {
			animation: none;
		}
	}
</style>
