<script lang="ts">
	// The Compose project an object belongs to (#6, #7): a Docker Manager-managed
	// stack links to it (its objects change through the stack); other
	// projects are named only.
	import Layers from '@lucide/svelte/icons/layers';
	import type { Schema } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { Badge } from '$lib/ui';

	interface Props {
		stack: Schema<'StackMembership'>;
		/** Show the Compose service too ("silo / silo-web"). */
		service?: boolean;
	}

	let { stack, service = false }: Props = $props();
	const label = $derived(
		service && stack.service ? `${stack.project} / ${stack.service}` : stack.project
	);
</script>

{#if stack.managed && stack.stackId}
	<a class="stack" href={routes.stack(stack.stackId)} title="Managed stack {stack.project}">
		<Badge tone="accent">
			<Layers size={13} strokeWidth={1.75} aria-hidden="true" />
			{label}<span class="sr-only"> (managed stack)</span>
		</Badge>
	</a>
{:else}
	<Badge title="Compose project {stack.project}, not managed by Docker Manager">
		<Layers size={13} strokeWidth={1.75} aria-hidden="true" />
		{label}<span class="sr-only"> (Compose project)</span>
	</Badge>
{/if}

<style>
	.stack {
		display: inline-flex;
		border-radius: var(--radius-sm);
	}

	.stack:hover :global(.badge) {
		border-color: var(--accent-text);
	}
</style>
