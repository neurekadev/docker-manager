<script lang="ts" module>
	import type { ButtonVariant } from './Button.svelte';

	/** The looks of a split button: the Deploy primary, secondary, or a lifecycle tone. */
	export type SplitButtonVariant = Extract<
		ButtonVariant,
		'primary' | 'secondary' | 'ok-soft' | 'danger-soft'
	>;
</script>

<script lang="ts">
	// Split button (#22: the stack header's Deploy, the file editor's
	// Format, the lifecycle button's Start/Stop): the main part runs the
	// default action; the attached chevron opens a menu of variants (Deploy,
	// Deploy with pull, Build and deploy). The soft tones (ok-soft,
	// danger-soft) say what the main part does to what runs (Start, Stop).
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import type { IconComponent } from '$lib/design/icons';
	import Button, { type ButtonSize } from './Button.svelte';
	import Menu from './Menu.svelte';
	import type { MenuEntry } from './menu';

	interface Props {
		label: string;
		icon?: IconComponent;
		items: MenuEntry[];
		/** Accessible name of the chevron, e.g. "More deploy options". */
		menuLabel: string;
		onclick?: () => void;
		variant?: SplitButtonVariant;
		size?: ButtonSize;
		loading?: boolean;
		/** Turns off the main part (and the chevron unless `menuDisabled` says otherwise). */
		disabled?: boolean;
		/** Turns off the chevron; defaults to `disabled`. */
		menuDisabled?: boolean;
		/** The main part's tooltip, e.g. why it is off. */
		title?: string;
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
		disabled = false,
		menuDisabled,
		title
	}: Props = $props();

	const chevronOff = $derived((menuDisabled ?? disabled) || loading);
</script>

<div class="split {variant} {size}" role="group" aria-label={label}>
	<Button {variant} {size} {icon} {loading} {disabled} {onclick} {title} class="main"
		>{label}</Button
	>
	<Menu {items} label={menuLabel}>
		{#snippet trigger(props)}
			<button
				{...props}
				type="button"
				class="chevron"
				aria-label={menuLabel}
				disabled={chevronOff}
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

	/* The soft tones: the chevron shares the tone; the main part's own border
	   is the divider. */
	.ok-soft .chevron {
		background: var(--ok-soft);
		border-color: var(--ok-border);
		border-left-color: transparent;
		color: var(--ok);
	}
	.ok-soft .chevron:hover:not(:disabled),
	.ok-soft .chevron[data-state='open'] {
		background: color-mix(in srgb, var(--ok-soft) 80%, var(--ok));
	}

	.danger-soft .chevron {
		background: var(--danger-soft);
		border-color: var(--danger-border);
		border-left-color: transparent;
		color: var(--danger);
	}
	.danger-soft .chevron:hover:not(:disabled),
	.danger-soft .chevron[data-state='open'] {
		background: color-mix(in srgb, var(--danger-soft) 80%, var(--danger));
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
