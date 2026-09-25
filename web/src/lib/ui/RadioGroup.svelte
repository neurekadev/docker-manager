<script lang="ts" module>
	export interface RadioOption {
		value: string;
		label: string;
		description?: string;
		disabled?: boolean;
	}
</script>

<script lang="ts">
	// Radio group (#22): native radios in a fieldset (arrow keys move and
	// select; the legend names the group).
	interface Props {
		label: string;
		options: RadioOption[];
		value?: string;
		name?: string;
		description?: string;
		error?: string | null;
		onchange?: (value: string) => void;
	}

	let {
		label,
		options,
		value = $bindable(''),
		name,
		description,
		error,
		onchange
	}: Props = $props();
	const uid = $props.id();
	const groupName = $derived(name ?? `rg-${uid}`);
</script>

<fieldset
	class="group"
	aria-describedby={[description ? `rg-${uid}-desc` : '', error ? `rg-${uid}-err` : '']
		.filter(Boolean)
		.join(' ') || undefined}
>
	<legend>{label}</legend>
	{#if description}<p class="desc" id="rg-{uid}-desc">{description}</p>{/if}
	{#each options as o (o.value)}
		<label class="option" class:disabled={o.disabled}>
			<input
				type="radio"
				name={groupName}
				value={o.value}
				bind:group={value}
				disabled={o.disabled}
				onchange={() => onchange?.(o.value)}
			/>
			<span class="text">
				<span class="label">{o.label}</span>
				{#if o.description}<span class="desc">{o.description}</span>{/if}
			</span>
		</label>
	{/each}
	{#if error}<p class="error" id="rg-{uid}-err">{error}</p>{/if}
</fieldset>

<style>
	.group {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		border: 0;
		min-width: 0;
	}

	legend {
		padding: 0;
		margin-bottom: 6px;
		color: var(--text-default);
		font-size: var(--text-body);
		font-weight: var(--weight-medium);
	}

	.option {
		display: flex;
		align-items: flex-start;
		gap: var(--space-2);
		cursor: pointer;
	}

	.option.disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	input {
		appearance: none;
		flex-shrink: 0;
		width: 16px;
		height: 16px;
		margin: 2px 0 0;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-full);
		background: var(--surface-raised);
		cursor: inherit;
	}

	input:checked {
		border: 5px solid var(--accent);
		background: var(--text-on-accent);
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

	.error {
		color: var(--danger);
		font-size: var(--text-caption);
	}
</style>
