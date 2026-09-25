<script lang="ts" module>
	export interface Crumb {
		label: string;
		href?: string;
	}
</script>

<script lang="ts">
	// Breadcrumbs (#22 top bar: "homelab / Stacks / Silo"). The last crumb is
	// the current page (aria-current, not a link).
	let { items, label = 'Breadcrumb' }: { items: Crumb[]; label?: string } = $props();
</script>

<nav aria-label={label} class="crumbs">
	<ol role="list">
		{#each items as crumb, i (i)}
			<li>
				{#if i === items.length - 1}
					<span aria-current="page" class="current">{crumb.label}</span>
				{:else if crumb.href}
					<a href={crumb.href}>{crumb.label}</a>
					<span class="sep" aria-hidden="true">/</span>
				{:else}
					<span>{crumb.label}</span>
					<span class="sep" aria-hidden="true">/</span>
				{/if}
			</li>
		{/each}
	</ol>
</nav>

<style>
	.crumbs {
		min-width: 0;
	}

	ol {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin: 0;
		min-width: 0;
		white-space: nowrap;
	}

	li {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
		color: var(--text-muted);
		font-size: var(--text-control);
	}

	a {
		color: var(--text-muted);
	}

	a:hover {
		color: var(--text-strong);
	}

	.current {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.sep {
		flex-shrink: 0;
		color: var(--text-faint);
	}

	/* Long names shorten with an ellipsis instead of running into the next
	   crumb. */
	a,
	li > span:not(.sep) {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	/* Phones: only the parent and the current page fit the top bar. */
	@media (max-width: 767px) {
		li:not(:nth-last-child(-n + 2)) {
			display: none;
		}
	}
</style>
