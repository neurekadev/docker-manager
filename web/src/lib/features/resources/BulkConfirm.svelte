<script lang="ts" generics="T">
	// Confirms a bulk action of a resource list (#6, #32, #22 polish): what
	// it runs on, what is left out and why (Docker Manager's own objects,
	// stack-managed containers, objects in use: reported, never silently
	// dropped) and how many it skips. `confirmText` asks for a typed
	// confirmation (removing volumes deletes data).
	import { ConfirmDialog, TypeToConfirm } from '$lib/ui';
	import type { BulkPlan } from './bulk';

	interface Props {
		open?: boolean;
		title: string;
		/** What happens, one line each ("Running containers get a stop signal."). */
		consequences?: string[];
		plan: BulkPlan<T>;
		name: (item: T) => string;
		confirmLabel: string;
		danger?: boolean;
		/** Text to type before confirming (high impact). */
		confirmText?: string;
		onconfirm: () => unknown | Promise<unknown>;
	}

	let {
		open = $bindable(false),
		title,
		consequences = [],
		plan,
		name,
		confirmLabel,
		danger = false,
		confirmText,
		onconfirm
	}: Props = $props();

	let typed = $state('');
	$effect(() => {
		if (!open) typed = '';
	});
	const ready = $derived(plan.run.length > 0 && (!confirmText || typed === confirmText));
</script>

<ConfirmDialog
	bind:open
	{title}
	consequences={plan.run.length ? consequences : []}
	message={plan.run.length ? undefined : 'None of the selected objects can take this action.'}
	{confirmLabel}
	tone={danger ? 'danger' : 'default'}
	canConfirm={ready}
	size="md"
	{onconfirm}
>
	{#if plan.run.length}
		<div class="group">
			<p class="head">Runs on {plan.run.length}</p>
			<ul role="list" aria-label="Runs On">
				{#each plan.run as item, i (i)}<li class="name">{name(item)}</li>{/each}
			</ul>
		</div>
	{/if}
	{#if plan.refused.length}
		<div class="group">
			<p class="head">Left Out ({plan.refused.length})</p>
			<ul role="list" aria-label="Left Out">
				{#each plan.refused as r, i (i)}
					<li>
						<span class="name">{name(r.item)}</span><span class="why">{r.reason}</span>
					</li>
				{/each}
			</ul>
		</div>
	{/if}
	{#if plan.skipped.length}
		<p class="skipped">
			{plan.skipped.length} skipped: the action does not apply to {plan.skipped.length === 1
				? 'it'
				: 'them'} ({plan.skipped.map(name).join(', ')}).
		</p>
	{/if}
	{#if confirmText && plan.run.length}
		<div class="type"><TypeToConfirm text={confirmText} bind:value={typed} /></div>
	{/if}
</ConfirmDialog>

<style>
	.group {
		margin-top: var(--space-3);
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
		padding: 0;
		overflow-y: auto;
		list-style: none;
	}

	li {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: var(--space-1) var(--space-3);
		padding: 6px var(--space-3);
	}

	.name {
		color: var(--text-strong);
		font-family: var(--font-mono);
		font-size: 12.5px;
		overflow-wrap: anywhere;
	}

	.why {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.skipped {
		margin-top: var(--space-3);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.type {
		margin-top: var(--space-4);
	}
</style>
