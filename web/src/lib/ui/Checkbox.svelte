<script lang="ts">
	// Checkbox (#22): a native input (form semantics, keyboard, screen
	// readers) with DockYard styling; `indeterminate` shows the mixed state
	// (aria-checked="mixed" via the DOM property).
	import type { HTMLInputAttributes } from 'svelte/elements';

	interface Props extends Omit<HTMLInputAttributes, 'type' | 'checked'> {
		checked?: boolean;
		indeterminate?: boolean;
		label: string;
		/** Keep the label for screen readers only (table row selection). */
		hideLabel?: boolean;
		description?: string;
	}

	let {
		checked = $bindable(false),
		indeterminate = false,
		label,
		hideLabel = false,
		description,
		id,
		disabled,
		...rest
	}: Props = $props();

	const uid = $props.id();
	const inputId = $derived(id ?? `cb-${uid}`);
	let el = $state<HTMLInputElement>();
	$effect(() => {
		if (el) el.indeterminate = indeterminate;
	});
</script>

<label class="check" class:disabled for={inputId}>
	<input
		bind:this={el}
		bind:checked
		id={inputId}
		type="checkbox"
		{disabled}
		aria-describedby={description ? `${inputId}-desc` : undefined}
		{...rest}
	/>
	<span class="text" class:sr-only={hideLabel}>
		<span class="label">{label}</span>
		{#if description}<span class="desc" id="{inputId}-desc">{description}</span>{/if}
	</span>
</label>

<style>
	.check {
		display: inline-flex;
		align-items: flex-start;
		gap: var(--space-2);
		cursor: pointer;
	}

	.disabled {
		cursor: not-allowed;
		opacity: 0.5;
	}

	input {
		appearance: none;
		flex-shrink: 0;
		width: 16px;
		height: 16px;
		margin: 2px 0 0;
		border: 1px solid var(--border-strong);
		border-radius: 4px;
		background: var(--surface-raised);
		cursor: inherit;
		transition:
			background-color var(--duration-fast) var(--ease-out),
			border-color var(--duration-fast) var(--ease-out);
	}

	input:hover:not(:disabled) {
		border-color: var(--text-faint);
	}

	input:checked,
	input:indeterminate {
		border-color: var(--accent);
		background-color: var(--accent);
		background-repeat: no-repeat;
		background-position: center;
	}

	input:checked {
		background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none'%3E%3Cpath d='M4 8.5l2.5 2.5L12 5.5' stroke='white' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
	}

	input:indeterminate {
		background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none'%3E%3Cpath d='M4.5 8h7' stroke='white' stroke-width='2' stroke-linecap='round'/%3E%3C/svg%3E");
	}

	.text {
		display: flex;
		flex-direction: column;
	}

	.label {
		color: var(--text-default);
		font-size: var(--text-control);
		line-height: var(--leading-control);
	}

	.desc {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}
</style>
