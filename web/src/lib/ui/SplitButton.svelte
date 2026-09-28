<script lang="ts">
	// Split button (#22: the stack header's Deploy, the file editor's
	// Format): the main part runs the default action; the attached chevron
	// opens a menu of variants (Deploy, Deploy with pull, Build and deploy).
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import type { IconComponent } from '$lib/design/icons';
	import Button, { type ButtonSize, type ButtonVariant } from './Button.svelte';
	import Menu from './Menu.svelte';
	import type { MenuEntry } from './menu';

	interface Props {
		label: string;
		icon?: IconComponent;
		items: MenuEntry[];
		/** Accessible name of the chevron, e.g. "More deploy options". */
		menuLabel: string;
		onclick?: () => void;
		variant?: Extract<ButtonVariant, 'primary' | 'secondary'>;
		size?: ButtonSize;
		loading?: boolean;
		disabled?: boolean;
	}

	let {
		label,
		icon,
		items,
		menuLabel,
		onclick,
		variant = 'primary',
		size = 'md',
		loading = false,
		disabled = false
	}: Props = $props();
</script>

<div class="split {variant} {size}" role="group" aria-label={label}>
	<Button {variant} {size} {icon} {loading} {disabled} {onclick} class="main">{label}</Button>
	<Menu {items} label={menuLabel}>
		{#snippet trigger(props)}
			<button
				{...props}
				type="button"
				class="chevron"
				aria-label={menuLabel}
				disabled={disabled || loading}
			>
				<ChevronDown size={16} strokeWidth={1.75} aria-hidden="true" />
			</button>
		{/snippet}
	</Menu>
</div>

<style>
	.split {
		display: inline-flex;
		align-items: stretch;
		border-radius: var(--radius-md);
	}

	.split :global(.main) {
		border-top-right-radius: 0;
		border-bottom-right-radius: 0;
	}

	.chevron {
		display: inline-grid;
		place-items: center;
		width: 30px;
		height: var(--control-height);
		padding: 0;
		border: 1px solid transparent;
		border-left: 1px solid rgb(255 255 255 / 0.18);
		border-radius: 0 var(--radius-md) var(--radius-md) 0;
		transition: background-color var(--duration-fast) var(--ease-out);
	}

	.primary .chevron {
		background: var(--accent);
		color: var(--text-on-accent);
	}
	.primary .chevron:hover:not(:disabled),
	.primary .chevron[data-state='open'] {
		background: var(--accent-hover);
	}

	.secondary .chevron {
		background: var(--surface-raised);
		border-color: var(--border-strong);
		color: var(--text-strong);
	}
	.secondary .chevron:hover:not(:disabled),
	.secondary .chevron[data-state='open'] {
		background: var(--surface-hover);
	}

	.sm .chevron {
		width: 26px;
		height: var(--control-height-sm);
		border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
	}

	.chevron:disabled {
		opacity: 0.5;
	}

	@media (pointer: coarse) {
		.chevron,
		.sm .chevron {
			min-height: var(--touch-target);
			width: var(--touch-target);
		}
	}
</style>
