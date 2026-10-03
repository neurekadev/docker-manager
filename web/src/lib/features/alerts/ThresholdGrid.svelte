<script lang="ts">
	// The levels of the alert thresholds as a compact grid: a row per
	// metric (Temperature (°C), Disk Space (% Used), Memory (% Used)) and
	// the columns Warning and Critical, one number field each, with its
	// error in words below it. The defaults card and the override dialog
	// share it; the dialog's empty fields show the default as placeholder.
	import { TextField } from '$lib/ui';
	import {
		THRESHOLD_METRICS,
		type ThresholdErrors,
		type ThresholdForm,
		type ThresholdKey
	} from './thresholds';

	interface Props {
		/** The group's accessible name ("Default Levels"). */
		label: string;
		form: ThresholdForm;
		errors?: ThresholdErrors;
		/** Shown in empty fields (an override's defaults). */
		placeholders?: Partial<Record<ThresholdKey, string>>;
		/** Every level optional (an override's). */
		optional?: boolean;
		onchange: (key: ThresholdKey, value: string) => void;
	}

	let { label, form, errors = {}, placeholders, optional = false, onchange }: Props = $props();

	const UNIT_WORDS: Record<string, string> = {
		temperature: '°C',
		diskSpace: '% Used',
		memory: '% Used'
	};
</script>

<div class="grid" role="group" aria-label={label}>
	<span></span>
	<span class="head" aria-hidden="true">Warning</span>
	<span class="head" aria-hidden="true">Critical</span>
	{#each THRESHOLD_METRICS as m (m.metric)}
		<span class="row" aria-hidden="true">{m.label}</span>
		{#each [{ key: m.warning, level: 'Warning' }, { key: m.critical, level: 'Critical' }] as f (f.key)}
			<div class="cell">
				<TextField
					label="{m.short} {f.level} ({UNIT_WORDS[m.metric]})"
					hideLabel
					type="number"
					inputmode="numeric"
					min="0"
					max={m.max}
					step="1"
					required={!optional}
					value={form[f.key]}
					placeholder={placeholders?.[f.key]}
					error={errors[f.key]}
					oninput={(e) => onchange(f.key, e.currentTarget.value)}
				/>
			</div>
		{/each}
	{/each}
</div>

<style>
	.grid {
		display: grid;
		grid-template-columns: minmax(0, max-content) repeat(2, minmax(5rem, 8rem));
		align-items: start;
		gap: var(--space-2) var(--space-3);
	}

	.head {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		font-weight: var(--weight-medium);
	}

	.row {
		/* Level with the input's text (36 px controls). */
		padding-top: var(--space-2);
		padding-right: var(--space-3);
		color: var(--text-default);
		white-space: nowrap;
	}

	.cell {
		min-width: 0;
	}

	/* Levels have at most three digits: narrow fields leave the metric
	   names room for one line ("Disk Space (% Used)"). */
	@media (max-width: 767px) {
		.grid {
			grid-template-columns: minmax(0, 1fr) repeat(2, 4.5rem);
		}

		.row {
			white-space: normal;
		}
	}
</style>
