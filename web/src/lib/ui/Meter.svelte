<script lang="ts">
	// Meter (#22 memory KPI: "1.8 GB / 8 GB" with a bar and the percentage).
	// A native-like role="meter" with value text; colour turns amber from
	// `warnAt` and red from `dangerAt` (the text still states the value).
	interface Props {
		value: number;
		max: number;
		label: string;
		/** Text for assistive technology, e.g. "1.8 GB of 8 GB". */
		valueText?: string;
		warnAt?: number;
		dangerAt?: number;
		showPercent?: boolean;
	}

	let {
		value,
		max,
		label,
		valueText,
		warnAt = 0.8,
		dangerAt = 0.95,
		showPercent = true
	}: Props = $props();
	const ratio = $derived(max > 0 ? Math.min(1, Math.max(0, value / max)) : 0);
	const tone = $derived(ratio >= dangerAt ? 'danger' : ratio >= warnAt ? 'warn' : 'ok');
</script>

<div class="meter-row">
	<div
		class="meter"
		role="meter"
		aria-label={label}
		aria-valuemin={0}
		aria-valuemax={max}
		aria-valuenow={value}
		aria-valuetext={valueText ?? `${Math.round(ratio * 100)}%`}
	>
		<span class="fill {tone}" style="width: {ratio * 100}%"></span>
	</div>
	{#if showPercent}<span class="pct num">{Math.round(ratio * 100)}%</span>{/if}
</div>

<style>
	.meter-row {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}

	.meter {
		flex: 1;
		height: 6px;
		overflow: hidden;
		border-radius: var(--radius-full);
		background: var(--surface-raised);
	}

	.fill {
		display: block;
		height: 100%;
		border-radius: inherit;
		background: var(--accent);
		transition: width var(--duration-open) var(--ease-out);
	}

	.fill.warn {
		background: var(--warn);
	}

	.fill.danger {
		background: var(--danger);
	}

	.pct {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}
</style>
