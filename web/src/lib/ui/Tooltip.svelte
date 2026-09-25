<script lang="ts">
	// Tooltip (#22): a short text label for a control (Bits UI behind our
	// styling). The trigger snippet receives props to spread on the focusable
	// element, so the tooltip opens on hover and keyboard focus. Tooltips
	// supplement accessible names, never replace them.
	import type { Snippet } from 'svelte';
	import { Tooltip as T } from 'bits-ui';

	interface Props {
		text: string;
		side?: 'top' | 'right' | 'bottom' | 'left';
		disabled?: boolean;
		delay?: number;
		trigger: Snippet<[Record<string, unknown>]>;
	}

	let { text, side = 'top', disabled = false, delay = 400, trigger }: Props = $props();
</script>

<T.Provider delayDuration={delay} {disabled}>
	<T.Root>
		<T.Trigger>
			{#snippet child({ props })}
				{@render trigger(props)}
			{/snippet}
		</T.Trigger>
		<T.Portal>
			<T.Content class="dy-tooltip" {side} sideOffset={6} collisionPadding={8}>
				{text}
			</T.Content>
		</T.Portal>
	</T.Root>
</T.Provider>

<style>
	:global(.dy-tooltip) {
		z-index: var(--z-tooltip);
		max-width: 280px;
		padding: 5px 8px;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
		color: var(--text-strong);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		font-weight: var(--weight-medium);
	}
</style>
