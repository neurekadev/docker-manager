<script lang="ts">
	// Renders the toast queue ($lib/ui/toast.svelte.ts). Two live regions:
	// polite for confirmations, assertive (role="alert") for errors.
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Info from '@lucide/svelte/icons/info';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import X from '@lucide/svelte/icons/x';
	import { toast as defaultToasts, type Toasts } from './toast.svelte';

	let { toasts = defaultToasts }: { toasts?: Toasts } = $props();

	const icons = { success: CircleCheck, error: CircleAlert, info: Info, warn: TriangleAlert };
</script>

<div class="toaster">
	<div class="region" role="status" aria-live="polite" aria-label="Notifications">
		{#each toasts.items.filter((t) => t.tone !== 'error') as t (t.id)}
			{@render item(t)}
		{/each}
	</div>
	<div class="region" role="alert" aria-live="assertive" aria-label="Errors">
		{#each toasts.items.filter((t) => t.tone === 'error') as t (t.id)}
			{@render item(t)}
		{/each}
	</div>
</div>

{#snippet item(t: (typeof toasts.items)[number])}
	{@const Icon = icons[t.tone]}
	<div class="toast {t.tone}">
		<Icon size={18} strokeWidth={1.75} aria-hidden="true" class="icon" />
		<div class="text">
			<p class="title">{t.title}</p>
			{#if t.body}<p class="body">{t.body}</p>{/if}
			{#if t.action}
				<button
					type="button"
					class="action"
					onclick={() => {
						t.action?.onclick();
						toasts.dismiss(t.id);
					}}>{t.action.label}</button
				>
			{/if}
		</div>
		<button
			type="button"
			class="close"
			aria-label="Dismiss"
			onclick={() => toasts.dismiss(t.id)}
		>
			<X size={16} strokeWidth={1.75} aria-hidden="true" />
		</button>
	</div>
{/snippet}

<style>
	.toaster {
		position: fixed;
		right: var(--space-4);
		bottom: var(--space-4);
		z-index: var(--z-toast);
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		width: min(380px, calc(100vw - 32px));
		pointer-events: none;
	}

	.region {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}

	.toast {
		display: flex;
		align-items: flex-start;
		gap: var(--space-3);
		padding: var(--space-3) var(--space-3) var(--space-3) var(--space-4);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
		pointer-events: auto;
		animation: toast-in var(--duration-open) var(--ease-out);
	}

	@keyframes toast-in {
		from {
			opacity: 0;
			transform: translateY(8px);
		}
	}

	.toast :global(.icon) {
		margin-top: 1px;
	}

	.success :global(.icon) {
		color: var(--ok);
	}
	.error {
		border-color: var(--danger-border);
	}
	.error :global(.icon) {
		color: var(--danger);
	}
	.info :global(.icon) {
		color: var(--info);
	}
	.warn :global(.icon) {
		color: var(--warn);
	}

	.text {
		flex: 1;
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-size: var(--text-control);
		font-weight: var(--weight-medium);
	}

	.body {
		margin-top: 2px;
		color: var(--text-muted);
	}

	.action {
		margin-top: var(--space-2);
		padding: 0;
		border: 0;
		background: none;
		color: var(--accent-text);
		font-weight: var(--weight-medium);
	}

	.close {
		display: grid;
		place-items: center;
		width: 24px;
		height: 24px;
		padding: 0;
		border: 0;
		border-radius: var(--radius-sm);
		background: none;
		color: var(--text-muted);
	}

	.close:hover {
		background: var(--surface-hover);
		color: var(--text-strong);
	}
</style>
