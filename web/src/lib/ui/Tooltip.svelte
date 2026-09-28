<script lang="ts">
	// Tooltip (#22): a short text label for a control (Bits UI behind our
	// styling). The trigger snippet receives props to spread on the focusable
	// element, so the tooltip opens on hover and keyboard focus. Tooltips
	// supplement accessible names, never replace them. The .dy-tooltip look
	// is shared with TooltipLayer (every title attribute; design/global.css).
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
