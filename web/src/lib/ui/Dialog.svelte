<script lang="ts">
	// Dialog (#22; Bits UI Dialog): modal, focus trapped inside, Escape and an
	// outside click close it (unless `dismissible` is false), and focus
	// returns to the element that opened it. Full-screen below 768 px.
	// `alert` gives role="alertdialog" (confirmations). Sizes: sm for
	// confirmations and one-field prompts, md for short forms, lg for forms
	// with several groups or a table, xl for editors laid out in columns
	// (policies, stacks).
	import type { Snippet } from 'svelte';
	import X from '@lucide/svelte/icons/x';
	import { AlertDialog, Dialog } from 'bits-ui';
	import IconButton from './IconButton.svelte';
	import { claimDialogLayer } from './layers';

	interface Props {
		open?: boolean;
		title: string;
		description?: string;
		size?: 'sm' | 'md' | 'lg' | 'xl';
		alert?: boolean;
		dismissible?: boolean;
		children?: Snippet;
		footer?: Snippet;
		/** Optional trigger; spread props on the opening button. */
		trigger?: Snippet<[Record<string, unknown>]>;
		onclose?: () => void;
	}

	let {
		open = $bindable(false),
		title,
		description,
		size = 'md',
		alert = false,
		dismissible = true,
		children,
		footer,
		trigger,
		onclose
	}: Props = $props();

	// Remember what had focus when the dialog opened, to return to it.
	let returnTo: HTMLElement | null = null;
	$effect.pre(() => {
		if (open && typeof document !== 'undefined') {
			returnTo =
				document.activeElement instanceof HTMLElement ? document.activeElement : null;
		}
	});

	// A dialog opened over another one (the step-up check over the dialog
	// that needs it) paints above it, whatever the DOM order (layers.ts).
	let layer = $state(0);
	$effect.pre(() => {
		if (!open) return;
		const claim = claimDialogLayer();
		layer = claim.layer;
		return claim.release;
	});
	const layerStyle = $derived(`--dy-layer: ${layer}`);

	function onOpenChange(o: boolean) {
		if (!o) onclose?.();
	}

	// Confirmations use Bits UI's AlertDialog (role="alertdialog"; an outside
	// click does not dismiss them).
	const D = $derived(alert ? AlertDialog : Dialog);
</script>

<D.Root bind:open {onOpenChange}>
	{#if trigger}
		<D.Trigger>
			{#snippet child({ props })}{@render trigger(props)}{/snippet}
		</D.Trigger>
	{/if}
	<D.Portal>
		<D.Overlay class="dy-overlay" style={layerStyle} />
		<D.Content
			class="dy-dialog {size}"
			style={layerStyle}
			onEscapeKeydown={(e) => {
				if (!dismissible) e.preventDefault();
			}}
			onInteractOutside={(e) => {
				if (!dismissible) e.preventDefault();
			}}
			onCloseAutoFocus={(e) => {
				if (returnTo && returnTo.isConnected) {
					e.preventDefault();
					returnTo.focus();
				}
			}}
		>
			<header class="dy-dialog-head">
				<div>
					<D.Title class="dy-dialog-title">{title}</D.Title>
					{#if description}
						<D.Description class="dy-dialog-desc">{description}</D.Description>
					{/if}
				</div>
				{#if dismissible && !alert}
					<Dialog.Close>
						{#snippet child({ props })}
							<IconButton
								{...props}
								label="Close"
								icon={X}
								size="sm"
								tooltip={false}
							/>
						{/snippet}
					</Dialog.Close>
				{/if}
			</header>
			{#if children}<div class="dy-dialog-body">{@render children()}</div>{/if}
			{#if footer}<footer class="dy-dialog-foot">{@render footer()}</footer>{/if}
		</D.Content>
	</D.Portal>
</D.Root>

<style>
	:global(.dy-overlay) {
		position: fixed;
		inset: 0;
		z-index: calc(var(--z-dialog) + var(--dy-layer, 0));
		background: var(--surface-overlay);
	}

	:global(.dy-overlay[data-state='open']) {
		animation: dy-fade-in var(--duration-open) var(--ease-out);
	}

	:global(.dy-dialog) {
		position: fixed;
		top: 50%;
		left: 50%;
		z-index: calc(var(--z-dialog) + var(--dy-layer, 0));
		display: flex;
		flex-direction: column;
		width: calc(100vw - 32px);
		max-height: calc(100dvh - 64px);
		transform: translate(-50%, -50%);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
		box-shadow: var(--shadow-float);
		outline: none;
	}

	:global(.dy-dialog.sm) {
		max-width: 480px;
	}
	:global(.dy-dialog.md) {
		max-width: 640px;
	}
	:global(.dy-dialog.lg) {
		max-width: 880px;
	}
	:global(.dy-dialog.xl) {
		max-width: 1160px;
	}

	:global(.dy-dialog[data-state='open']) {
		animation: dy-dialog-in var(--duration-open) var(--ease-out);
	}

	@keyframes -global-dy-fade-in {
		from {
			opacity: 0;
		}
	}

	@keyframes -global-dy-dialog-in {
		from {
			opacity: 0;
			transform: translate(-50%, calc(-50% + 8px));
		}
	}

	:global(.dy-dialog-head) {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: var(--space-4);
		padding: var(--space-5) var(--space-5) var(--space-3);
	}

	:global(.dy-dialog-title) {
		color: var(--text-strong);
		font-size: var(--text-section);
		line-height: var(--leading-section);
		font-weight: var(--weight-semibold);
	}

	:global(.dy-dialog-desc) {
		margin-top: 2px;
		color: var(--text-muted);
		font-size: var(--text-body);
	}

	:global(.dy-dialog-body) {
		padding: 0 var(--space-5) var(--space-5);
		overflow-y: auto;
		color: var(--text-default);
	}

	:global(.dy-dialog-foot) {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-2);
		padding: var(--space-4) var(--space-5);
		border-top: 1px solid var(--border-subtle);
	}

	@media (max-width: 767px) {
		:global(.dy-dialog) {
			top: 0;
			left: 0;
			width: 100vw;
			max-width: none !important;
			height: 100dvh;
			max-height: none;
			transform: none;
			border-radius: 0;
		}

		:global(.dy-dialog[data-state='open']) {
			animation: dy-fade-in var(--duration-open) var(--ease-out);
		}

		:global(.dy-dialog-body) {
			flex: 1;
		}
	}
</style>
