<script lang="ts" module>
	/** What a field passes to its control. */
	export interface FieldControl {
		id: string;
		describedBy: string | undefined;
		invalid: boolean;
	}
</script>

<script lang="ts">
	// Field (#22): label, optional description, the control and its error.
	// Required fields carry `required` on the control (no visual asterisk);
	// optional ones set `optional`: a muted "Optional" after the label (the
	// description never says it). `info` puts an (i) after the label for an
	// explanation needed only now and then (InfoTip); keep `description`
	// for what the user needs to fill the field in.
	// The control receives id, aria-describedby and aria-invalid through the
	// snippet argument; errors say what is wrong and how to fix it.
	import type { Snippet } from 'svelte';
	import InfoTip from './InfoTip.svelte';
	import './field.css';

	interface Props {
		label: string;
		description?: string;
		/** An explanation behind an (i) after the label. */
		info?: string;
		/** Shows a muted "Optional" after the label. */
		optional?: boolean;
		error?: string | null;
		required?: boolean;
		/** Keep the label for screen readers only. */
		hideLabel?: boolean;
		id?: string;
		children: Snippet<[FieldControl]>;
		/** Content right of the label (e.g. a "Forgot password?" link). */
		aside?: Snippet;
	}

	let {
		label,
		description,
		info,
		optional = false,
		error,
		required = false,
		hideLabel = false,
		id,
		children,
		aside
	}: Props = $props();
	const uid = $props.id();
	const controlId = $derived(id ?? `f-${uid}`);
	const describedBy = $derived(
		[
			description ? `${controlId}-desc` : '',
			info ? `${controlId}-info` : '',
			error ? `${controlId}-err` : ''
		]
			.filter(Boolean)
			.join(' ') || undefined
	);
</script>

<div class="field" data-required={required || undefined}>
	<div class="label-row" class:sr-only={hideLabel}>
		<span class="label-start">
			<label for={controlId}>
				{label}
			</label>
			{#if optional}<span class="optional" aria-hidden="true">Optional</span>{/if}
			{#if info}<InfoTip id="{controlId}-info" text={info} />{/if}
		</span>
		{#if aside}{@render aside()}{/if}
	</div>
	{#if description}<p class="desc" id="{controlId}-desc">{description}</p>{/if}
	{@render children({ id: controlId, describedBy, invalid: !!error })}
	{#if error}<p class="error" id="{controlId}-err">{error}</p>{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}

	.label-row {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-2);
	}

	.label-start {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
	}

	.optional {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	label {
		color: var(--text-default);
		font-size: var(--text-body);
		line-height: var(--leading-body);
		font-weight: var(--weight-medium);
	}

	.desc {
		margin-top: -2px;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.error {
		color: var(--danger);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}
</style>
