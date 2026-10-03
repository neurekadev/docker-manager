<script lang="ts">
	// Multi-line text field (#22).
	import type { HTMLTextareaAttributes } from 'svelte/elements';
	import Field from './Field.svelte';

	interface Props extends Omit<HTMLTextareaAttributes, 'value'> {
		label: string;
		value?: string;
		description?: string;
		/** An explanation behind an (i) after the label. */
		info?: string;
		/** Shows a muted "Optional" after the label. */
		optional?: boolean;
		error?: string | null;
		mono?: boolean;
	}

	let {
		label,
		value = $bindable(''),
		info,
		optional = false,
		description,
		error,
		mono = false,
		required = false,
		id,
		rows = 4,
		...rest
	}: Props = $props();
</script>

<Field {label} {description} {info} {optional} {error} required={!!required} id={id ?? undefined}>
	{#snippet children(c)}
		<textarea
			bind:value
			id={c.id}
			class="dy-input"
			class:mono
			{rows}
			aria-describedby={c.describedBy}
			aria-invalid={c.invalid || undefined}
			{required}
			{...rest}></textarea>
	{/snippet}
</Field>
