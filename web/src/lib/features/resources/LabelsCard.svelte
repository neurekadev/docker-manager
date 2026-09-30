<script lang="ts">
	// The labels of a container, image, volume or network (#6, #22
	// polish): labels someone chose first; those Docker, Compose, image
	// builders and Docker Manager set themselves (com.docker.compose.*,
	// org.opencontainers.*, …) folded behind a disclosure. A volume's
	// Compose labels (declared in its stack's Compose file after the volume
	// was created; Docker keeps a volume's labels, Docker Manager honors
	// them) are their own group with an (i) saying so. Keys get a column
	// wide enough to read them.
	import { Card } from '$lib/ui';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { COMPOSE_LABELS_INFO, splitLabels } from './model';

	interface Props {
		labels: Record<string, string> | undefined;
		/** A volume's Compose labels (not on the volume, honored anyway). */
		composeLabels?: Record<string, string>;
		/** Accessible name of the lists, e.g. "Labels of pihole". */
		label: string;
	}

	let { labels, composeLabels, label }: Props = $props();
	const groups = $derived(splitLabels(labels));
	const compose = $derived(
		Object.entries(composeLabels ?? {}).sort(([a], [b]) => a.localeCompare(b))
	);
</script>

{#snippet list(entries: [string, string][], name: string)}
	<dl class="labels" aria-label={name}>
		{#each entries as [k, v] (k)}
			<div class="row">
				<dt class="mono">{k}</dt>
				<dd class="mono">
					{#if v}{v}{:else}<span class="muted">empty</span>{/if}
				</dd>
			</div>
		{/each}
	</dl>
{/snippet}

<Card title="Labels">
	{#if groups.user.length}
		{@render list(groups.user, label)}
	{:else if compose.length}
		<p class="muted">None on the volume itself.</p>
	{:else if groups.system.length}
		<p class="muted">Only the labels Docker and Compose set themselves.</p>
	{:else}
		<p class="muted">No labels.</p>
	{/if}
	{#if compose.length}
		<div class="system">
			<Disclosure
				open
				summary="{compose.length} Compose {compose.length === 1 ? 'label' : 'labels'}"
				hint={COMPOSE_LABELS_INFO}
			>
				{@render list(compose, `${label}: from the Compose file`)}
			</Disclosure>
		</div>
	{/if}
	{#if groups.system.length}
		<div class="system">
			<Disclosure
				summary="{groups.system.length} system {groups.system.length === 1
					? 'label'
					: 'labels'}"
			>
				{@render list(groups.system, `${label}: set by Docker and Compose`)}
			</Disclosure>
		</div>
	{/if}
</Card>

<style>
	.labels {
		display: grid;
		margin: 0;
	}

	.row {
		display: grid;
		grid-template-columns: minmax(160px, 45%) minmax(0, 1fr);
		gap: var(--space-3);
		padding: 6px 0;
		border-top: 1px solid var(--border-subtle);
		font-size: 12.5px;
	}

	.row:first-child {
		border-top: 0;
		padding-top: 0;
	}

	dt {
		min-width: 0;
		color: var(--text-muted);
		overflow-wrap: anywhere;
	}

	dd {
		min-width: 0;
		margin: 0;
		color: var(--text-default);
		overflow-wrap: anywhere;
	}

	.system {
		margin-top: var(--space-3);
	}

	@media (max-width: 767px) {
		.row {
			grid-template-columns: minmax(0, 1fr);
			gap: 2px;
		}
	}
</style>
