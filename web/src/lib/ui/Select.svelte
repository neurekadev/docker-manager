<script lang="ts" module>
	import type { IconComponent } from '$lib/design/icons';

	export interface SelectOption {
		value: string;
		label: string;
		disabled?: boolean;
		/** A decorative icon before the label (in the list and the trigger). */
		icon?: IconComponent;
	}
</script>

<script lang="ts">
	// Select (#22; Bits UI Select): a single choice from a short list in
	// Docker Manager styling (a themed listbox, never the browser's native
	// popup). Typeahead, arrow keys, Home/End, Enter/Space and Escape work
	// as on a native select; the trigger shows the chosen label (or the
	// placeholder). For long or searchable lists use Combobox.
	import Check from '@lucide/svelte/icons/check';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import { Select } from 'bits-ui';
	import Field from './Field.svelte';

	interface Props {
		label: string;
		options: SelectOption[];
		value?: string;
		description?: string;
		/** An explanation behind an (i) after the label. */
		info?: string;
		/** Shows a muted "Optional" after the label. */
		optional?: boolean;
		error?: string | null;
		hideLabel?: boolean;
		placeholder?: string;
		required?: boolean;
		disabled?: boolean;
		id?: string;
		name?: string;
		/** Tooltip of the trigger (e.g. the filter's name when the label is hidden). */
		title?: string;
		/** Called with the new value after the user picks an option. */
		onchange?: (value: string) => void;
	}

	let {
		label,
		options,
		value = $bindable(''),
		info,
		optional = false,
		description,
		error,
		hideLabel = false,
		placeholder,
		required = false,
		disabled = false,
		id,
		name,
		title,
		onchange
	}: Props = $props();

	const selected = $derived(options.find((o) => o.value === value));
	const items = $derived(
		options.map((o) => ({ value: o.value, label: o.label, disabled: o.disabled }))
	);
</script>

<Field {label} {description} {info} {optional} {error} {hideLabel} {required} {id}>
	{#snippet children(c)}
		<Select.Root
			type="single"
			bind:value
			{items}
			{disabled}
			{required}
			{name}
			onValueChange={(v) => onchange?.(v)}
		>
			<!-- role combobox: the select-only combobox pattern (announced with its value). -->
			<Select.Trigger
				id={c.id}
				role="combobox"
				class="dy-input dy-select-trigger"
				aria-describedby={c.describedBy}
				aria-invalid={c.invalid || undefined}
				{title}
			>
				{#if selected?.icon}
					{@const Icon = selected.icon}
					<span class="dy-select-icon" aria-hidden="true"
						><Icon size={16} strokeWidth={1.75} /></span
					>
				{/if}
				<span class="dy-select-value" class:placeholder={!selected}
					>{selected?.label ?? placeholder ?? ''}</span
				>
				<ChevronDown size={16} strokeWidth={1.75} aria-hidden="true" />
			</Select.Trigger>
			<Select.Portal>
				<Select.Content class="dy-select-content" sideOffset={6} collisionPadding={8}>
					<Select.Viewport>
						{#each options as o (o.value)}
							<Select.Item
								value={o.value}
								label={o.label}
								disabled={o.disabled}
								class="dy-select-item"
							>
								{#snippet children({ selected: on })}
									{#if o.icon}
										{@const Icon = o.icon}
										<span class="dy-select-icon" aria-hidden="true"
											><Icon size={16} strokeWidth={1.75} /></span
										>
									{/if}
									<span class="dy-select-label">{o.label}</span>
									{#if on}<Check
											size={16}
											strokeWidth={1.75}
											aria-hidden="true"
										/>{/if}
								{/snippet}
							</Select.Item>
						{/each}
					</Select.Viewport>
				</Select.Content>
			</Select.Portal>
		</Select.Root>
	{/snippet}
</Field>

<style>
	:global(.dy-select-trigger) {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
		padding-right: 10px;
		text-align: left;
		cursor: pointer;
	}

	:global(.dy-select-trigger > svg) {
		flex-shrink: 0;
		color: var(--text-muted);
		transition: transform var(--duration-fast) var(--ease-out);
	}

	:global(.dy-select-trigger[data-state='open'] > svg) {
		transform: rotate(180deg);
	}

	.dy-select-value {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dy-select-value.placeholder {
		color: var(--text-muted);
	}

	:global(.dy-select-content) {
		z-index: var(--z-menu);
		min-width: var(--bits-select-anchor-width);
		max-width: min(420px, calc(100vw - 16px));
		max-height: min(320px, var(--bits-select-content-available-height));
		overflow-y: auto;
		padding: var(--space-1);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
		outline: none;
	}

	:global(.dy-select-content[data-state='open']) {
		animation: dy-select-in var(--duration-open) var(--ease-out);
	}

	@keyframes -global-dy-select-in {
		from {
			opacity: 0;
			transform: translateY(-4px);
		}
		to {
			opacity: 1;
			transform: none;
		}
	}

	:global(.dy-select-item) {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 32px;
		padding: 6px var(--space-2);
		border-radius: var(--radius-sm);
		color: var(--text-default);
		font-size: var(--text-control);
		line-height: var(--leading-control);
		cursor: pointer;
		user-select: none;
		outline: none;
	}

	:global(.dy-select-item[data-highlighted]) {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	:global(.dy-select-item[data-selected]) {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	:global(.dy-select-item[data-selected] > svg) {
		color: var(--accent-text);
	}

	:global(.dy-select-item[data-disabled]) {
		opacity: 0.45;
		cursor: not-allowed;
	}

	.dy-select-icon {
		display: inline-flex;
		flex-shrink: 0;
		color: var(--text-muted);
	}

	.dy-select-label {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	@media (pointer: coarse) {
		:global(.dy-select-item) {
			min-height: var(--touch-target);
		}
	}
</style>
