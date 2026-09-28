<script lang="ts">
	// The heading of the public sign-in and onboarding pages (#16, #22):
	// the page title (h1, the small page-title size: the panel is narrow and
	// the logo above already names the product), an optional lead sentence
	// and something before the title (a back link).
	import type { Snippet } from 'svelte';

	interface Props {
		title: string;
		/** One sentence under the title; a snippet when it needs markup. */
		lead?: string | Snippet;
		/** Above the title, e.g. a back link. */
		before?: Snippet;
	}

	let { title, lead, before }: Props = $props();
</script>

<header class="auth-header">
	{#if before}{@render before()}{/if}
	<h1>{title}</h1>
	{#if typeof lead === 'string'}
		<p class="lead">{lead}</p>
	{:else if lead}
		<div class="lead">{@render lead()}</div>
	{/if}
</header>

<style>
	h1 {
		font-size: var(--text-title-sm);
		line-height: var(--leading-title-sm);
	}

	.lead {
		margin-top: var(--space-1);
		color: var(--text-muted);
		font-size: var(--text-control);
		line-height: var(--leading-control);
	}
</style>
