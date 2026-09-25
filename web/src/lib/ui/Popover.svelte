<script lang="ts">
	// Popover (#22; Bits UI Popover): non-modal floating content anchored to a
	// trigger (the notices list, the environment switcher). Escape and an
	// outside click close it; focus returns to the trigger.
	import type { Snippet } from 'svelte';
	import { Popover } from 'bits-ui';

	interface Props {
		open?: boolean;
		/** Accessible name of the popover content. */
		label: string;
		trigger: Snippet<[Record<string, unknown>]>;
		children: Snippet;
		align?: 'start' | 'center' | 'end';
		side?: 'top' | 'right' | 'bottom' | 'left';
		width?: string;
	}

	let {
		open = $bindable(false),
		label,
		trigger,
		children,
		align = 'end',
		side = 'bottom',
		width = '360px'
	}: Props = $props();
</script>

<Popover.Root bind:open>
	<Popover.Trigger>
		{#snippet child({ props })}{@render trigger(props)}{/snippet}
	</Popover.Trigger>
	<Popover.Portal>
		<Popover.Content
			class="dy-popover"
			{align}
			{side}
			sideOffset={8}
			collisionPadding={8}
			aria-label={label}
			style="--popover-width: {width}"
		>
			{@render children()}
		</Popover.Content>
	</Popover.Portal>
</Popover.Root>

<style>
	:global(.dy-popover) {
		z-index: var(--z-menu);
		width: min(var(--popover-width), calc(100vw - 16px));
		max-height: min(560px, calc(100dvh - 96px));
		overflow: auto;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
		outline: none;
	}

	:global(.dy-popover[data-state='open']) {
		animation: dy-menu-in var(--duration-open) var(--ease-out);
	}
</style>
