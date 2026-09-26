<script lang="ts">
	// Docker Manager logo lockup (#22): the whale mark (the 64 px app icon,
	// precached for offline boot) and the product name, no badge (#25 Q8:
	// no tier). `mark` alone for the icon rail.
	let { mark = false, href = '/' }: { mark?: boolean; href?: string } = $props();
</script>

<a {href} class="logo" aria-label="Docker Manager, go to the dashboard">
	<img src="/icons/pwa-64x64.png" width="32" height="32" alt="" aria-hidden="true" />
	{#if !mark}<span class="name">Docker Manager</span>{/if}
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

	img {
		flex-shrink: 0;
	}

	.name {
		font-size: 18px;
		line-height: 24px;
		font-weight: var(--weight-semibold);
		letter-spacing: -0.01em;
		white-space: nowrap;
	}
</style>
