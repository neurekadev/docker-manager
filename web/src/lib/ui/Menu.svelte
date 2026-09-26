<script lang="ts">
	// Dropdown menu (#22; Bits UI DropdownMenu behind Docker Manager styling). The
	// trigger snippet gets the props to spread on its button (usually an
	// IconButton or Button). Keyboard: Enter/Space/ArrowDown open, arrows move,
	// typeahead, Escape closes and returns focus to the trigger.
	import type { Snippet } from 'svelte';
	import { DropdownMenu } from 'bits-ui';
	import MenuItems from './MenuItems.svelte';
	import type { MenuEntry } from './menu';

	interface Props {
		items: MenuEntry[];
		trigger: Snippet<[Record<string, unknown>]>;
		/** Accessible name of the menu itself (e.g. "Actions for silo-web"). */
		label?: string;
		align?: 'start' | 'center' | 'end';
		side?: 'top' | 'right' | 'bottom' | 'left';
		open?: boolean;
	}

	let {
		items,
		trigger,
		label,
		align = 'end',
		side = 'bottom',
		open = $bindable(false)
	}: Props = $props();
</script>

<DropdownMenu.Root bind:open>
	<DropdownMenu.Trigger>
		{#snippet child({ props })}
			{@render trigger(props)}
		{/snippet}
	</DropdownMenu.Trigger>
	<DropdownMenu.Portal>
		<DropdownMenu.Content
			class="dy-menu"
			{align}
			{side}
			sideOffset={6}
			collisionPadding={8}
			aria-label={label}
		>
			<MenuItems {items} />
		</DropdownMenu.Content>
	</DropdownMenu.Portal>
</DropdownMenu.Root>
