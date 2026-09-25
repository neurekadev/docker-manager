<script lang="ts">
	// Right-click (and long-press) menu (#22, #15 file actions). Every item
	// must also be reachable without a right-click: offer the same entries in
	// a visible overflow Menu on the row (touch and keyboard users).
	import type { Snippet } from 'svelte';
	import { ContextMenu } from 'bits-ui';
	import MenuItems from './MenuItems.svelte';
	import type { MenuEntry } from './menu';

	interface Props {
		items: MenuEntry[];
		label?: string;
		/** The area that opens the menu; spread props on its root element. */
		children: Snippet<[Record<string, unknown>]>;
	}

	let { items, label, children }: Props = $props();
</script>

<ContextMenu.Root>
	<ContextMenu.Trigger>
		{#snippet child({ props })}
			{@render children(props)}
		{/snippet}
	</ContextMenu.Trigger>
	<ContextMenu.Portal>
		<ContextMenu.Content class="dy-menu" collisionPadding={8} aria-label={label}>
			<MenuItems {items} />
		</ContextMenu.Content>
	</ContextMenu.Portal>
</ContextMenu.Root>
