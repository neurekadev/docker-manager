<script lang="ts">
	// DockYard logo lockup (#22): the stacked-cube mark and the product name,
	// no badge (#25 Q8: no tier). `mark` alone for the icon rail.
	let { mark = false, href = '/' }: { mark?: boolean; href?: string } = $props();
</script>

<a {href} class="logo" aria-label="DockYard, go to the dashboard">
	<svg width="28" height="28" viewBox="0 0 28 28" aria-hidden="true">
		<path d="M14 2.5 24.5 8.5 14 14.5 3.5 8.5Z" fill="#5b92fd" />
		<path d="M3.5 8.5 14 14.5v11L3.5 19.5Z" fill="#2566fd" />
		<path d="M24.5 8.5 14 14.5v11l10.5-6Z" fill="#1d4fd0" />
		<path d="M14 14.5v11" stroke="#0b1016" stroke-opacity=".35" stroke-width=".75" />
	</svg>
	{#if !mark}<span class="name">DockYard</span>{/if}
</a>

<style>
	.logo {
		display: inline-flex;
		align-items: center;
		gap: 10px;
		height: 40px;
		padding: 0 var(--space-2);
		border-radius: var(--radius-md);
		color: var(--text-strong);
		text-decoration: none;
	}

	.logo:hover {
		text-decoration: none;
	}

	.name {
		font-size: 18px;
		line-height: 24px;
		font-weight: var(--weight-semibold);
		letter-spacing: -0.01em;
	}
</style>
