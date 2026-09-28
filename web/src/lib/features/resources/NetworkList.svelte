<script lang="ts">
	// The networks of a container or service in a table cell (#6, #7): each
	// network links to its page with the address on it beside it; past
	// `max` networks, "+N" with every other network and address in its
	// tooltip and for screen readers. A muted dash without any network.
	import { routes } from '$lib/routes';
	import type { NetworkEntry } from './model';

	interface Props {
		environmentId: string;
		networks: NetworkEntry[];
		max?: number;
	}

	let { environmentId, networks, max = 2 }: Props = $props();
	const shown = $derived(networks.slice(0, max));
	const rest = $derived(networks.slice(max));
	const text = (n: NetworkEntry) =>
		n.addresses.length ? `${n.name}: ${n.addresses.join(', ')}` : n.name;
</script>

{#if networks.length}
	<span class="networks">
		{#each shown as n (n.name)}
			<span class="net">
				<a class="name" href={routes.network(environmentId, n.name)}>{n.name}</a>
				{#if n.addresses.length}<span
						class="addr mono"
						title={n.addresses.length > 1 ? n.addresses.join('\n') : undefined}
						>{n.addresses[0]}{#if n.addresses.length > 1}<span
								class="muted"
								aria-hidden="true"
							>
								+{n.addresses.length - 1}</span
							><span class="sr-only">, {n.addresses.slice(1).join(', ')}</span
							>{/if}</span
					>{/if}
			</span>
		{/each}
		{#if rest.length}
			<span class="more muted" title={rest.map(text).join('\n')}
				>+{rest.length} more<span class="sr-only">: {rest.map(text).join('; ')}</span></span
			>
		{/if}
	</span>
{:else}<span class="muted">—</span>{/if}

<style>
	.networks {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.net {
		display: flex;
		align-items: baseline;
		gap: var(--space-2);
		min-width: 0;
		white-space: nowrap;
	}

	.name {
		overflow: hidden;
		color: var(--text-default);
		text-overflow: ellipsis;
		text-decoration: none;
	}

	.name:hover {
		color: var(--accent-text);
		text-decoration: underline;
	}

	.addr {
		color: var(--text-muted);
	}
</style>
