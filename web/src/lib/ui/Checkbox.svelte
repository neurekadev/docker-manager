<script lang="ts">
	// Checkbox (#22): a native input (form semantics, keyboard, screen
	// readers) with Docker Manager styling; `indeterminate` shows the mixed state
	// (aria-checked="mixed" via the DOM property). `icon` puts a decorative
	// glyph before the label ("What to Send": the kind of event's icon).
	// `info` puts an (i) after the label (InfoTip), outside the label so it
	// stays out of the checkbox's name.
	import type { HTMLInputAttributes } from 'svelte/elements';
	import type { IconComponent } from '$lib/design/icons';
	import InfoTip from './InfoTip.svelte';

	interface Props extends Omit<HTMLInputAttributes, 'type' | 'checked'> {
		checked?: boolean;
		indeterminate?: boolean;
		label: string;
		/** A decorative glyph before the label. */
		icon?: IconComponent;
		/** Keep the label for screen readers only (table row selection). */
		hideLabel?: boolean;
		description?: string;
		/** An explanation behind an (i) after the label. */
		info?: string;
	}

	let {
		checked = $bindable(false),
		indeterminate = false,
		label,
		icon: Icon,
		hideLabel = false,
		description,
		info,
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

{#snippet box()}
	<label class="check" class:disabled for={inputId}>
		<input
			bind:this={el}
			bind:checked
			id={inputId}
			type="checkbox"
			{disabled}
			aria-describedby={[description ? `${inputId}-desc` : '', info ? `${inputId}-info` : '']
				.filter(Boolean)
				.join(' ') || undefined}
			{...rest}
		/>
		<span class="text" class:sr-only={hideLabel}>
			<span class="label"
				>{#if Icon}<Icon
						class="label-icon"
						size={14}
						strokeWidth={1.75}
						aria-hidden="true"
					/>{/if}{label}</span
			>
			{#if description}<span class="desc" id="{inputId}-desc">{description}</span>{/if}
		</span>
	</label>
{/snippet}

{#if info}
	<span class="check-row">
		{@render box()}
		<span class="tip"><InfoTip id="{inputId}-info" text={info} /></span>
	</span>
{:else}
	{@render box()}
{/if}

<style>
	.check-row {
		display: inline-flex;
		align-items: flex-start;
		gap: var(--space-2);
	}

	.tip {
		display: inline-flex;
		align-items: center;
		min-height: var(--leading-body);
	}

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

	.label :global(.label-icon) {
		margin-right: var(--space-2);
		color: var(--text-muted);
		vertical-align: -2px;
	}

	.desc {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}
</style>
