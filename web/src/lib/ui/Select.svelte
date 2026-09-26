<script lang="ts" module>
	export interface SelectOption {
		value: string;
		label: string;
		disabled?: boolean;
	}
</script>

<script lang="ts">
	// Select (#22): a native <select> (best mobile and assistive support) in
	// Docker Manager styling. For long or searchable lists use Combobox.
	import type { HTMLSelectAttributes } from 'svelte/elements';
	import Field from './Field.svelte';

	interface Props extends Omit<HTMLSelectAttributes, 'value'> {
		label: string;
		options: SelectOption[];
		value?: string;
		description?: string;
		error?: string | null;
		hideLabel?: boolean;
		placeholder?: string;
	}

	let {
		label,
		options,
		value = $bindable(''),
		description,
		error,
		hideLabel = false,
		placeholder,
		required = false,
		id,
		...rest
	}: Props = $props();
</script>

<Field {label} {description} {error} {hideLabel} required={!!required} id={id ?? undefined}>
	{#snippet children(c)}
		<select
			bind:value
			id={c.id}
			class="dy-input"
			aria-describedby={c.describedBy}
			aria-invalid={c.invalid || undefined}
			{required}
			{...rest}
		>
			{#if placeholder}<option value="" disabled>{placeholder}</option>{/if}
			{#each options as o (o.value)}
				<option value={o.value} disabled={o.disabled}>{o.label}</option>
			{/each}
		</select>
	{/snippet}
</Field>
