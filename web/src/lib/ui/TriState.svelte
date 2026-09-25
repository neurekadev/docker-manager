<script lang="ts" module>
	export type TriValue = 'inherit' | 'allow' | 'deny';
</script>

<script lang="ts">
	// Tri-state permission control (#17 user overrides): Inherit / Allow /
	// Deny as a segmented radio group. With Inherit selected it states the
	// effective value and where it comes from, so the effect is never hidden.
	interface Props {
		/** Names the capability and scope, e.g. "Restart containers on homelab". */
		label: string;
		value?: TriValue;
		/** The effective decision while inheriting. */
		inherited?: 'allow' | 'deny';
		/** Where the inherited decision comes from, e.g. "group Operators". */
		inheritedFrom?: string;
		/** High-risk capability: the Allow choice is marked. */
		highRisk?: boolean;
		disabled?: boolean;
		onchange?: (value: TriValue) => void;
	}

	let {
		label,
		value = $bindable('inherit'),
		inherited = 'deny',
		inheritedFrom,
		highRisk = false,
		disabled = false,
		onchange
	}: Props = $props();
	const uid = $props.id();

	const options: { value: TriValue; label: string }[] = [
		{ value: 'inherit', label: 'Inherit' },
		{ value: 'allow', label: 'Allow' },
		{ value: 'deny', label: 'Deny' }
	];
	const effective = $derived(value === 'inherit' ? inherited : value);
	const explanation = $derived(
		value === 'inherit'
			? `Inherits ${inherited === 'allow' ? 'Allow' : 'Deny'}${inheritedFrom ? ` from ${inheritedFrom}` : ''}`
			: `${value === 'allow' ? 'Allowed' : 'Denied'} for this user, whatever the group grants`
	);
</script>

<div
	class="tri"
	role="radiogroup"
	aria-label={label}
	aria-describedby="tri-{uid}-why"
	data-effective={effective}
>
	<div class="segments">
		{#each options as o (o.value)}
			<label class="seg {o.value}" class:checked={value === o.value} class:disabled>
				<input
					type="radio"
					name="tri-{uid}"
					value={o.value}
					bind:group={value}
					{disabled}
					onchange={() => onchange?.(o.value)}
				/>
				<span>{o.label}</span>
				{#if o.value === 'allow' && highRisk}<span class="risk">High risk</span>{/if}
			</label>
		{/each}
	</div>
	<span class="why" id="tri-{uid}-why">{explanation}</span>
</div>

<style>
	.tri {
		display: inline-flex;
		flex-direction: column;
		gap: 4px;
	}

	.segments {
		display: inline-flex;
		padding: 2px;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
	}

	.seg {
		position: relative;
		display: inline-flex;
		align-items: center;
		gap: 6px;
		height: 26px;
		padding: 0 10px;
		border-radius: 4px;
		color: var(--text-muted);
		font-size: var(--text-body);
		font-weight: var(--weight-medium);
		cursor: pointer;
		user-select: none;
	}

	.seg input {
		position: absolute;
		inset: 0;
		margin: 0;
		opacity: 0;
		cursor: inherit;
	}

	.seg:has(input:focus-visible) {
		outline: var(--focus-ring);
		outline-offset: 1px;
	}

	.seg:hover:not(.disabled) {
		color: var(--text-strong);
	}

	.seg.checked.inherit {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	.seg.checked.allow {
		background: var(--ok-soft);
		color: var(--ok);
	}

	.seg.checked.deny {
		background: var(--danger-soft);
		color: var(--danger);
	}

	.seg.disabled {
		cursor: not-allowed;
		opacity: 0.5;
	}

	.risk {
		padding: 0 4px;
		border-radius: 3px;
		background: var(--warn-soft);
		color: var(--warn);
		font-size: 10px;
		line-height: 14px;
	}

	.why {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}
</style>
