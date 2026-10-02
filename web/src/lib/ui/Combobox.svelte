<script lang="ts">
	// Combobox (#22; Bits UI Combobox): a searchable single choice, e.g. a
	// registry connection or an IANA time zone. Filtering is a
	// case-insensitive substring match on the label.
	import Check from '@lucide/svelte/icons/check';
	import ChevronsUpDown from '@lucide/svelte/icons/chevrons-up-down';
	import { Combobox } from 'bits-ui';
	import Field from './Field.svelte';
	import type { SelectOption } from './Select.svelte';

	interface Props {
		label: string;
		options: SelectOption[];
		value?: string;
		placeholder?: string;
		description?: string;
		error?: string | null;
		/** Shown when nothing matches. */
		emptyText?: string;
		onchange?: (value: string) => void;
	}

	let {
		label,
		options,
		value = $bindable(''),
		placeholder,
		description,
		error,
		emptyText = 'No matches',
		onchange
	}: Props = $props();

	let search = $state('');
	const filtered = $derived(
		search
			? options.filter((o) => o.label.toLowerCase().includes(search.toLowerCase()))
			: options
	);
	const selectedLabel = $derived(options.find((o) => o.value === value)?.label ?? '');
</script>

<Field {label} {description} {error}>
	{#snippet children(c)}
		<Combobox.Root
			type="single"
			bind:value
			items={options}
			inputValue={selectedLabel}
			onValueChange={(v) => onchange?.(v)}
			onOpenChange={(o) => {
				if (!o) search = '';
			}}
		>
			<div class="wrap">
				<Combobox.Input
					id={c.id}
					class="dy-input"
					{placeholder}
					aria-describedby={c.describedBy}
					aria-invalid={c.invalid || undefined}
					oninput={(e) => (search = e.currentTarget.value)}
				/>
				<Combobox.Trigger class="dy-combobox-trigger" aria-label="Show Options for {label}">
					<ChevronsUpDown size={16} strokeWidth={1.75} aria-hidden="true" />
				</Combobox.Trigger>
			</div>
			<Combobox.Portal>
				<Combobox.Content
					class="dy-menu dy-combobox-content"
					sideOffset={6}
					collisionPadding={8}
				>
					<Combobox.Viewport>
						{#each filtered as o (o.value)}
							<Combobox.Item
								value={o.value}
								label={o.label}
								disabled={o.disabled}
								class="dy-menu-item"
							>
								{#snippet children({ selected })}
									<span class="dy-menu-label">{o.label}</span>
									{#if selected}<Check
											size={16}
											strokeWidth={1.75}
											aria-hidden="true"
										/>{/if}
								{/snippet}
							</Combobox.Item>
						{:else}
							<div class="dy-menu-heading">{emptyText}</div>
						{/each}
					</Combobox.Viewport>
				</Combobox.Content>
			</Combobox.Portal>
		</Combobox.Root>
	{/snippet}
</Field>

<style>
	.wrap {
		position: relative;
	}

	.wrap :global(.dy-input) {
		padding-right: 36px;
	}

	:global(.dy-combobox-trigger) {
		position: absolute;
		top: 50%;
		right: 4px;
		display: grid;
		place-items: center;
		width: 28px;
		height: 28px;
		transform: translateY(-50%);
		border: 0;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--text-muted);
	}

	:global(.dy-combobox-content) {
		width: var(--bits-combobox-anchor-width);
		max-height: min(320px, var(--bits-combobox-content-available-height));
		overflow-y: auto;
	}
</style>
