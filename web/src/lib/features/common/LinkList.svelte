<script lang="ts">
	// The links of a stack or template (documentation, website, repository),
	// compact on one wrapping row: each opens in a new tab (noopener,
	// noreferrer) with an external-link icon, its label or else the
	// address's host, and the full address as tooltip. Nothing renders
	// without links.
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import { linkText, type WebLink } from './links';

	let { links = [], label = 'Links' }: { links?: WebLink[]; label?: string } = $props();
</script>

{#if links.length}
	<ul class="links" aria-label={label}>
		{#each links as l, i (`${i}:${l.url}`)}
			<li>
				<a href={l.url} target="_blank" rel="noopener noreferrer" title={l.url}>
					<ExternalLink size={14} strokeWidth={1.75} aria-hidden="true" />
					<span class="text">{linkText(l)}</span>
					<span class="sr-only">(opens in a new tab)</span>
				</a>
			</li>
		{/each}
	</ul>
{/if}

<style>
	.links {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-4);
		margin: 0;
		padding: 0;
		list-style: none;
		min-width: 0;
	}

	li {
		min-width: 0;
		max-width: 100%;
	}

	a {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		max-width: 100%;
		color: var(--accent-text);
		font-size: var(--text-control);
		text-decoration: none;
	}

	a:hover .text {
		text-decoration: underline;
	}

	.text {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	@media (pointer: coarse) {
		a {
			min-height: 40px;
		}
	}
</style>
