<script lang="ts">
	// Password field with a reveal toggle (#22). The value is never logged or
	// stored by the component; `autocomplete` should be current-password or
	// new-password so password managers work.
	import Eye from '@lucide/svelte/icons/eye';
	import EyeOff from '@lucide/svelte/icons/eye-off';
	import type { HTMLInputAttributes } from 'svelte/elements';
	import Field from './Field.svelte';
	import IconButton from './IconButton.svelte';

	interface Props extends Omit<HTMLInputAttributes, 'value' | 'type'> {
		label: string;
		value?: string;
		description?: string;
		error?: string | null;
		autocomplete?: 'current-password' | 'new-password' | 'off';
		ref?: HTMLInputElement | null;
		/** Shown in plain text (the eye toggles it); e.g. right after "Show address". */
		revealed?: boolean;
	}

	let {
		label,
		value = $bindable(''),
		description,
		error,
		autocomplete = 'current-password',
		required = false,
		id,
		ref = $bindable(null),
		revealed = $bindable(false),
		...rest
	}: Props = $props();
</script>

<Field {label} {description} {error} required={!!required} id={id ?? undefined}>
	{#snippet children(c)}
		<div class="wrap">
			<input
				bind:this={ref}
				bind:value
				type={revealed ? 'text' : 'password'}
				id={c.id}
				class="dy-input"
				{autocomplete}
				spellcheck="false"
				autocapitalize="off"
				aria-describedby={c.describedBy}
				aria-invalid={c.invalid || undefined}
				{required}
				{...rest}
			/>
			<span class="reveal">
				<IconButton
					size="sm"
					label={revealed ? 'Hide password' : 'Show password'}
					icon={revealed ? EyeOff : Eye}
					pressed={revealed}
					onclick={() => (revealed = !revealed)}
				/>
			</span>
		</div>
	{/snippet}
</Field>

<style>
	.wrap {
		position: relative;
	}

	.wrap :global(.dy-input) {
		padding-right: 40px;
	}

	.reveal {
		position: absolute;
		top: 50%;
		right: 3px;
		transform: translateY(-50%);
	}
</style>
