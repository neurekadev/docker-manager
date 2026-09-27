<script lang="ts" module>
	export interface AffectedResource {
		label: string;
		/** e.g. "running", "3 containers", "volume". */
		detail?: string;
	}
</script>

<script lang="ts">
	// Type-to-confirm for high-impact actions (#22): lists what will happen
	// and every affected resource, and enables the danger button only after
	// the user typed `confirmText` (usually the resource name) exactly. The
	// name is shown as a code block with a copy button (TypeToConfirm).
	import type { Snippet } from 'svelte';
	import ConfirmDialog from './ConfirmDialog.svelte';
	import TypeToConfirm from './TypeToConfirm.svelte';

	interface Props {
		open?: boolean;
		title: string;
		/** What will happen, e.g. "Removes 3 containers. Volumes and files are kept." */
		consequences: string[];
		affected?: AffectedResource[];
		confirmText: string;
		confirmLabel: string;
		onconfirm: () => unknown | Promise<unknown>;
		/** Extra content before the affected list (e.g. a "migrate first" offer). */
		extra?: Snippet;
		/** Another condition besides the typed text (e.g. a preview without blockers). */
		canConfirm?: boolean;
		/** md or lg when extra shows a form or preview. */
		size?: 'sm' | 'md' | 'lg';
	}

	let {
		open = $bindable(false),
		title,
		consequences,
		affected = [],
		confirmText,
		confirmLabel,
		onconfirm,
		extra,
		canConfirm = true,
		size = 'sm'
	}: Props = $props();

	let typed = $state('');
	$effect(() => {
		if (!open) typed = '';
	});
	const matches = $derived(typed === confirmText);
</script>

<ConfirmDialog
	bind:open
	{title}
	{consequences}
	{confirmLabel}
	tone="danger"
	{onconfirm}
	{size}
	canConfirm={matches && canConfirm}
>
	{#if extra}{@render extra()}{/if}
	{#if affected.length}
		<div class="affected">
			<p class="head">Affected ({affected.length})</p>
			<ul role="list">
				{#each affected as a (a.label)}
					<li>
						<span class="name">{a.label}</span>{#if a.detail}<span class="detail"
								>{a.detail}</span
							>{/if}
					</li>
				{/each}
			</ul>
		</div>
	{/if}
	<div class="type">
		<TypeToConfirm text={confirmText} bind:value={typed} />
	</div>
</ConfirmDialog>

<style>
	.affected {
		margin-top: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-canvas);
	}

	.head {
		padding: var(--space-2) var(--space-3);
		border-bottom: 1px solid var(--border-subtle);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	ul {
		max-height: 160px;
		margin: 0;
		overflow-y: auto;
	}

	li {
		display: flex;
		justify-content: space-between;
		gap: var(--space-3);
		padding: 6px var(--space-3);
	}

	.name {
		color: var(--text-strong);
		font-family: var(--font-mono);
		font-size: 12.5px;
	}

	.detail {
		color: var(--text-muted);
	}

	.type {
		margin-top: var(--space-4);
	}
</style>
