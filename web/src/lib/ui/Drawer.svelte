<script lang="ts">
	// Drawer (#22): a modal side or bottom sheet (Bits UI Dialog) for detail
	// panes, the log drawer and the narrow-layout navigation. Focus is trapped
	// and returns to the opener; Escape and the overlay close it.
	import type { Snippet } from 'svelte';
	import X from '@lucide/svelte/icons/x';
	import { Dialog } from 'bits-ui';
	import IconButton from './IconButton.svelte';

	interface Props {
		open?: boolean;
		title: string;
		/** Keep the title for screen readers only (navigation drawer). */
		hideTitle?: boolean;
		side?: 'left' | 'right' | 'bottom';
		/** Width (left/right) or height (bottom), CSS length. */
		size?: string;
		children: Snippet;
		footer?: Snippet;
		onclose?: () => void;
	}

	let {
		open = $bindable(false),
		title,
		hideTitle = false,
		side = 'right',
		size = side === 'bottom' ? '45dvh' : '420px',
		children,
		footer,
		onclose
	}: Props = $props();

	let returnTo: HTMLElement | null = null;
	$effect.pre(() => {
		if (open && typeof document !== 'undefined') {
			returnTo =
				document.activeElement instanceof HTMLElement ? document.activeElement : null;
		}
	});
</script>

<Dialog.Root bind:open onOpenChange={(o) => !o && onclose?.()}>
	<Dialog.Portal>
		<Dialog.Overlay class="dy-overlay dy-drawer-overlay" />
		<Dialog.Content
			class="dy-drawer {side}"
			style="--drawer-size: {size}"
			onCloseAutoFocus={(e) => {
				if (returnTo && returnTo.isConnected) {
					e.preventDefault();
					returnTo.focus();
				}
			}}
		>
			<header class="dy-drawer-head" class:hidden-title={hideTitle}>
				<Dialog.Title class="dy-drawer-title {hideTitle ? 'sr-only' : ''}"
					>{title}</Dialog.Title
				>
				<Dialog.Close>
					{#snippet child({ props })}
						<IconButton {...props} label="Close" icon={X} size="sm" tooltip={false} />
					{/snippet}
				</Dialog.Close>
			</header>
			<div class="dy-drawer-body">{@render children()}</div>
			{#if footer}<footer class="dy-drawer-foot">{@render footer()}</footer>{/if}
		</Dialog.Content>
	</Dialog.Portal>
</Dialog.Root>

<style>
	/* Below its drawer (the shared overlay sits at the dialog layer). */
	:global(.dy-overlay.dy-drawer-overlay) {
		z-index: var(--z-drawer);
	}

	:global(.dy-drawer) {
		position: fixed;
		z-index: var(--z-drawer);
		display: flex;
		flex-direction: column;
		background: var(--surface-shell);
		border: 0 solid var(--border-strong);
		box-shadow: var(--shadow-float);
		outline: none;
	}

	:global(.dy-drawer.right),
	:global(.dy-drawer.left) {
		top: 0;
		bottom: 0;
		width: min(var(--drawer-size), 100vw);
	}

	:global(.dy-drawer.right) {
		right: 0;
		border-left-width: 1px;
	}

	:global(.dy-drawer.left) {
		left: 0;
		border-right-width: 1px;
	}

	:global(.dy-drawer.bottom) {
		left: 0;
		right: 0;
		bottom: 0;
		height: min(var(--drawer-size), 100dvh);
		border-top-width: 1px;
		border-radius: var(--radius-lg) var(--radius-lg) 0 0;
	}

	:global(.dy-drawer.right[data-state='open']) {
		animation: dy-drawer-right var(--duration-open) var(--ease-out);
	}
	:global(.dy-drawer.left[data-state='open']) {
		animation: dy-drawer-left var(--duration-open) var(--ease-out);
	}
	:global(.dy-drawer.bottom[data-state='open']) {
		animation: dy-drawer-bottom var(--duration-open) var(--ease-out);
	}

	@keyframes -global-dy-drawer-right {
		from {
			transform: translateX(24px);
			opacity: 0;
		}
	}
	@keyframes -global-dy-drawer-left {
		from {
			transform: translateX(-24px);
			opacity: 0;
		}
	}
	@keyframes -global-dy-drawer-bottom {
		from {
			transform: translateY(24px);
			opacity: 0;
		}
	}

	:global(.dy-drawer-head) {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		min-height: var(--topbar-height);
		padding: 0 var(--space-3) 0 var(--space-5);
		border-bottom: 1px solid var(--border-subtle);
	}

	:global(.dy-drawer-head.hidden-title) {
		justify-content: flex-end;
		border-bottom: 0;
	}

	:global(.dy-drawer-title) {
		color: var(--text-strong);
		font-size: var(--text-section);
		font-weight: var(--weight-semibold);
	}

	:global(.dy-drawer-body) {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
	}

	:global(.dy-drawer-foot) {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-2);
		padding: var(--space-3) var(--space-5);
		border-top: 1px solid var(--border-subtle);
	}
</style>
