<script lang="ts">
	// Removing an image, volume or network (#6: clear deletion consequences,
	// usage checks; #32: Docker Manager's own objects are refused). Shows the
	// server's removal preview: blockers (with the reason each request would
	// get) or the consequences, confirmed by typing the name.
	import type { Schema } from '$lib/api/client';
	import { sentence } from './refusals';
	import { Button, DestructiveConfirm, Dialog, type AffectedResource } from '$lib/ui';

	interface Props {
		open?: boolean;
		/** "image", "volume", "network". */
		kind: string;
		name: string;
		removal: Schema<'Removal'> | undefined;
		affected?: AffectedResource[];
		confirmLabel: string;
		onconfirm: () => unknown | Promise<unknown>;
	}

	let {
		open = $bindable(false),
		kind,
		name,
		removal,
		affected = [],
		confirmLabel,
		onconfirm
	}: Props = $props();

	const blocked = $derived(!!removal && removal.blockers.length > 0);
</script>

{#if blocked && removal}
	<Dialog bind:open title="{name} can't be removed" size="sm" alert>
		<ul class="blockers" aria-label="Why the {kind} can't be removed">
			{#each removal.blockers as b, i (i)}
				<li>
					{sentence(b.message)}
					{#if b.code === 'protected'}
						<span class="muted"
							>Docker Manager never removes its own {kind}s, for anyone; use Docker on
							the host if you really need to.</span
						>
					{/if}
				</li>
			{/each}
		</ul>
		{#snippet footer()}
			<Button variant="secondary" onclick={() => (open = false)}>Close</Button>
		{/snippet}
	</Dialog>
{:else}
	<DestructiveConfirm
		bind:open
		title="Remove {name}?"
		consequences={removal?.consequences.length
			? removal.consequences.map(sentence)
			: [`The ${kind} is deleted from this environment.`]}
		affected={affected.length ? affected : [{ label: name, detail: kind }]}
		confirmText={name}
		{confirmLabel}
		{onconfirm}
	/>
{/if}

<style>
	.blockers {
		display: grid;
		gap: var(--space-2);
		padding-left: 18px;
		color: var(--text-default);
	}

	.blockers span {
		display: block;
		margin-top: 2px;
	}
</style>
