<script lang="ts" module>
	export type ButtonVariant =
		'primary' | 'secondary' | 'ghost' | 'danger' | 'danger-soft' | 'ok-soft';
	export type ButtonSize = 'sm' | 'md';
</script>

<script lang="ts">
	// Button (#22). Labels name the result ("Deploy", "Save Changes"), never
	// "Submit". With href it renders a link styled as a button. loading keeps
	// the label, swaps the icon for a spinner and sets aria-busy. iconOnPhones
	// shows only the icon below 768 px (a page header with many actions); the
	// label stays the accessible name.
	import type { Snippet } from 'svelte';
	import type { HTMLButtonAttributes } from 'svelte/elements';
	import type { IconComponent } from '$lib/design/icons';
	import Spinner from './Spinner.svelte';

	interface Props extends Omit<HTMLButtonAttributes, 'children'> {
		variant?: ButtonVariant;
		size?: ButtonSize;
		icon?: IconComponent;
		iconEnd?: IconComponent;
		loading?: boolean;
		href?: string;
		block?: boolean;
		iconOnPhones?: boolean;
		ref?: HTMLElement | null;
		children?: Snippet;
	}

	let {
		variant = 'secondary',
		size = 'md',
		icon: Icon,
		iconEnd: IconEnd,
		loading = false,
		href,
		block = false,
		iconOnPhones = false,
		type = 'button',
		disabled = false,
		ref = $bindable(null),
		class: cls = '',
		children,
		...rest
	}: Props = $props();
</script>

{#snippet content()}
	{#if loading}
		<Spinner size={16} />
	{:else if Icon}
		<Icon size={16} strokeWidth={1.75} aria-hidden="true" />
	{/if}
	{#if children}<span class="label">{@render children()}</span>{/if}
	{#if IconEnd}
		<IconEnd size={16} strokeWidth={1.75} aria-hidden="true" />
	{/if}
{/snippet}

{#if href && !disabled}
	<a
		bind:this={ref}
		{href}
		class="btn {variant} {size} {cls}"
		class:block
		class:icon-phones={iconOnPhones && (Icon || loading)}
		aria-busy={loading || undefined}
		{...rest as Record<string, unknown>}
	>
		{@render content()}
	</a>
{:else}
	<button
		bind:this={ref}
		{type}
		class="btn {variant} {size} {cls}"
		class:block
		class:icon-phones={iconOnPhones && (Icon || loading)}
		disabled={disabled || loading}
		aria-busy={loading || undefined}
		{...rest}
	>
		{@render content()}
	</button>
{/if}

<style>
	.btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-2);
		height: var(--control-height);
		padding: 0 14px;
		border: 1px solid transparent;
		border-radius: var(--radius-md);
		font-size: var(--text-control);
		line-height: var(--leading-control);
		font-weight: var(--weight-medium);
		white-space: nowrap;
		text-decoration: none;
		user-select: none;
		transition:
			background-color var(--duration-fast) var(--ease-out),
			border-color var(--duration-fast) var(--ease-out),
			color var(--duration-fast) var(--ease-out);
	}

	.btn:hover {
		text-decoration: none;
	}

	.sm {
		height: var(--control-height-sm);
		padding: 0 10px;
		font-size: var(--text-body);
		border-radius: var(--radius-sm);
	}

	.block {
		display: flex;
		width: 100%;
	}

	.label {
		overflow: hidden;
		text-overflow: ellipsis;
	}

	@media (max-width: 767px) {
		.icon-phones {
			width: var(--control-height);
			padding: 0;
		}
		.icon-phones.sm {
			width: var(--control-height-sm);
		}
		.icon-phones .label {
			position: absolute;
			width: 1px;
			height: 1px;
			overflow: hidden;
			clip: rect(0, 0, 0, 0);
		}
	}

	.primary {
		background: var(--accent);
		color: var(--text-on-accent);
	}
	.primary:hover:not(:disabled) {
		background: var(--accent-hover);
	}

	.secondary {
		background: var(--surface-raised);
		border-color: var(--border-strong);
		color: var(--text-strong);
	}
	.secondary:hover:not(:disabled) {
		background: var(--surface-hover);
	}

	.ghost {
		background: transparent;
		color: var(--text-default);
	}
	.ghost:hover:not(:disabled) {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	.danger {
		background: var(--danger);
		color: var(--surface-canvas);
	}
	.danger:hover:not(:disabled) {
		background: color-mix(in srgb, var(--danger) 88%, white);
	}

	.danger-soft {
		background: var(--danger-soft);
		border-color: var(--danger-border);
		color: var(--danger);
	}
	.danger-soft:hover:not(:disabled) {
		background: color-mix(in srgb, var(--danger-soft) 80%, var(--danger));
	}

	/* Start (the lifecycle button's main part while nothing runs). */
	.ok-soft {
		background: var(--ok-soft);
		border-color: var(--ok-border);
		color: var(--ok);
	}
	.ok-soft:hover:not(:disabled) {
		background: color-mix(in srgb, var(--ok-soft) 80%, var(--ok));
	}

	.btn:disabled {
		opacity: 0.5;
	}

	.btn[aria-busy='true'] {
		opacity: 0.85;
		cursor: progress;
	}

	@media (pointer: coarse) {
		.btn {
			min-height: var(--touch-target);
		}
	}
</style>
