<script lang="ts">
	// Switch (#22: the log viewer's Follow toggle, policy enable). A button
	// with role="switch" and aria-checked; Space and Enter toggle it. Use for
	// settings that apply immediately; use Checkbox inside forms.
	interface Props {
		checked?: boolean;
		label: string;
		hideLabel?: boolean;
		description?: string;
		disabled?: boolean;
		onchange?: (checked: boolean) => void;
	}

	let {
		checked = $bindable(false),
		label,
		hideLabel = false,
		description,
		disabled = false,
		onchange
	}: Props = $props();
	const uid = $props.id();

	function toggle() {
		checked = !checked;
		onchange?.(checked);
	}
</script>

<div class="switch-row">
	<button
		type="button"
		role="switch"
		aria-checked={checked}
		aria-labelledby="sw-{uid}-label"
		aria-describedby={description ? `sw-${uid}-desc` : undefined}
		class="switch"
		{disabled}
		onclick={toggle}
	>
		<span class="thumb" aria-hidden="true"></span>
	</button>
	<span class="text" class:sr-only={hideLabel}>
		<span id="sw-{uid}-label" class="label">{label}</span>
		{#if description}<span id="sw-{uid}-desc" class="desc">{description}</span>{/if}
	</span>
</div>

<style>
	.switch-row {
		display: inline-flex;
		align-items: flex-start;
		gap: var(--space-2);
	}

	.switch {
		position: relative;
		flex-shrink: 0;
		width: 32px;
		height: 18px;
		margin-top: 1px;
		padding: 0;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-full);
		background: var(--surface-raised);
		transition: background-color var(--duration-fast) var(--ease-out);
	}

	.thumb {
		position: absolute;
		top: 2px;
		left: 2px;
		width: 12px;
		height: 12px;
		border-radius: var(--radius-full);
		background: var(--text-muted);
		transition:
			transform var(--duration-fast) var(--ease-out),
			background-color var(--duration-fast) var(--ease-out);
	}

	.switch[aria-checked='true'] {
		border-color: var(--accent);
		background: var(--accent);
	}

	.switch[aria-checked='true'] .thumb {
		transform: translateX(14px);
		background: var(--text-on-accent);
	}

	.switch:disabled {
		opacity: 0.5;
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
</style>
