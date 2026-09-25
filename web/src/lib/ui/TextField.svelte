<script lang="ts">
	// Text input field (#22). `mono` for identifiers, paths and codes.
	import type { HTMLInputAttributes } from 'svelte/elements';
	import Field from './Field.svelte';

	interface Props extends Omit<HTMLInputAttributes, 'value'> {
		label: string;
		value?: string;
		description?: string;
		error?: string | null;
		hideLabel?: boolean;
		mono?: boolean;
		ref?: HTMLInputElement | null;
	}

	let {
		label,
		value = $bindable(''),
		description,
		error,
		hideLabel = false,
		mono = false,
		required = false,
		type = 'text',
		id,
		ref = $bindable(null),
		...rest
	}: Props = $props();
</script>

<Field {label} {description} {error} {hideLabel} required={!!required} id={id ?? undefined}>
	{#snippet children(c)}
		<input
			bind:this={ref}
			bind:value
			{type}
			id={c.id}
			class="dy-input"
			class:mono
			aria-describedby={c.describedBy}
			aria-invalid={c.invalid || undefined}
			{required}
			{...rest}
		/>
	{/snippet}
</Field>
