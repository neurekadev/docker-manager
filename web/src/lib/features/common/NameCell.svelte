<script lang="ts">
	// The first cell of a list row: the name (a link to the detail page when
	// href is set) and a muted secondary line, as in the mockup's services
	// table ("silo-web" / "Web frontend").
	import type { Snippet } from 'svelte';

	interface Props {
		name: string;
		href?: string;
		sub?: string;
		mono?: boolean;
		subMono?: boolean;
		/** Extra content on the secondary line (badges). */
		extra?: Snippet;
	}

	let { name, href, sub, mono = false, subMono = false, extra }: Props = $props();
</script>

<div class="name-cell">
	{#if href}
		<a class="name" class:mono {href}>{name}</a>
	{:else}
		<span class="name" class:mono>{name}</span>
	{/if}
	{#if sub || extra}
		<div class="sub">
			{#if sub}<span class:mono={subMono}>{sub}</span>{/if}
			{#if extra}{@render extra()}{/if}
		</div>
	{/if}
</div>

<style>
	.name-cell {
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		overflow-wrap: anywhere;
	}

	a.name:hover {
		color: var(--accent-text);
	}

	.sub {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin-top: 2px;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}
</style>
