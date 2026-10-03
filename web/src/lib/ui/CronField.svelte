<script lang="ts">
	// Cron field (#13): a preset ("Hourly", "Daily", "Weekly" with minute,
	// time and day fields) or a custom five-field expression, plus the IANA
	// time zone, with the next runs previewed by the manager (POST
	// /schedules/previews, the one cron parser, #13) so DST gaps and repeats
	// are explained exactly as they will run. The presets write the cron
	// expression (bind:cron), so callers only ever see an expression; one
	// the presets cannot edit opens as Custom. The preview is debounced;
	// server validation errors show inline.
	import { untrack } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import { ApiRequestError } from '$lib/api/client';
	import { schedulePreviewQuery } from '$lib/api/queries';
	import Combobox from './Combobox.svelte';
	import Field from './Field.svelte';
	import Select from './Select.svelte';
	import TextField from './TextField.svelte';
	import {
		WEEKDAY_OPTIONS,
		buildCron,
		describeCron,
		parseCronPreset,
		type CronPresetKind
	} from './cron';
	import { formatDateTime } from './format';

	interface Props {
		label?: string;
		cron?: string;
		timeZone?: string;
		/** Schedule kind (#13), to explain its missed-run behaviour. */
		kind?: string;
		description?: string;
		/** Debounce of the preview request in ms. */
		debounce?: number;
		timeZones?: string[];
	}

	let {
		label = 'Schedule',
		cron = $bindable('0 3 * * *'),
		timeZone = $bindable('UTC'),
		kind,
		description = 'Five fields: minute, hour, day of month, month, day of week.',
		debounce = 300,
		timeZones
	}: Props = $props();

	// Unique per instance: a form may hold several schedules (checks, runs).
	const uid = $props.id();
	const previewId = `${uid}-cron-preview`;

	const KINDS = [
		{ value: 'hourly', label: 'Hourly' },
		{ value: 'daily', label: 'Daily' },
		{ value: 'weekly', label: 'Weekly' },
		{ value: 'custom', label: 'Custom' }
	];

	// The preset fields, from the expression the caller passed in.
	const initial = untrack(() => parseCronPreset(cron));
	let preset = $state<CronPresetKind>(initial.kind);
	let minute = $state(initial.minute);
	let hour = $state(initial.hour);
	let day = $state(initial.day);
	// The expression last written or seen (plain bookkeeping, not state).
	let written = untrack(() => cron.trim());

	function write() {
		if (preset === 'custom') return;
		const next = buildCron({ kind: preset, minute, hour, day });
		written = next;
		cron = next;
	}

	// The caller replaced the expression (defaults arrived, a reset): show it
	// in the presets when they can edit it. Custom stays custom while typed.
	$effect(() => {
		const c = cron.trim();
		if (c === written) return;
		written = c;
		if (untrack(() => preset) === 'custom') return;
		const p = parseCronPreset(c);
		preset = p.kind;
		minute = p.minute;
		hour = p.hour;
		day = p.day;
	});

	function setKind(v: string) {
		preset = v as CronPresetKind;
		write();
	}

	const pad = (n: number) => String(n).padStart(2, '0');

	function setTime(v: string) {
		const m = v.match(/^(\d{1,2}):(\d{2})/);
		if (!m) return;
		hour = Math.min(23, Number(m[1]));
		minute = Math.min(59, Number(m[2]));
		write();
	}

	// A number input binds numbers (null while empty).
	function setMinute(v: string | number | null) {
		if (v === null || v === '') return;
		const n = Number(v);
		if (!Number.isInteger(n) || n < 0 || n > 59) return;
		minute = n;
		write();
	}

	function setDay(v: string) {
		day = Number(v);
		write();
	}

	const words = $derived(describeCron(cron, timeZone));

	const zones = $derived.by(() => {
		const list =
			timeZones ??
			(typeof Intl.supportedValuesOf === 'function'
				? Intl.supportedValuesOf('timeZone')
				: ['UTC']);
		// Browsers omit aliases such as "UTC" from their list: keep the
		// saved zone selectable so it is shown, not a blank field.
		const all = timeZone && !list.includes(timeZone) ? [timeZone, ...list] : list;
		return all.map((z) => ({ value: z, label: z }));
	});

	// Debounced copy of the inputs that drives the preview query.
	let settled = $state({ cron: '', timeZone: '' });
	$effect(() => {
		const next = { cron: cron.trim(), timeZone };
		const t = setTimeout(() => (settled = next), debounce);
		return () => clearTimeout(t);
	});

	const preview = createQuery(() => schedulePreviewQuery(settled.cron, settled.timeZone, kind));

	const cronError = $derived.by(() => {
		const e = preview.error;
		if (!(e instanceof ApiRequestError) || !e.apiError)
			return e ? 'The preview is unavailable right now.' : null;
		const detail = e.apiError.details.find(
			(d) => d.field.endsWith('.cron') || d.field.endsWith('.timeZone')
		);
		return detail?.message ?? e.apiError.message;
	});
</script>

<fieldset class="cron">
	<legend>{label}</legend>
	<div class="presets" class:custom={preset === 'custom'}>
		<Select label="Repeats" options={KINDS} value={preset} onchange={setKind} />
		{#if preset === 'hourly'}
			<TextField
				label="At Minute"
				type="number"
				min="0"
				max="59"
				inputmode="numeric"
				bind:value={() => String(minute), setMinute}
			/>
		{:else if preset === 'weekly'}
			<Select label="Day" options={WEEKDAY_OPTIONS} value={String(day)} onchange={setDay} />
		{/if}
		{#if preset === 'daily' || preset === 'weekly'}
			<TextField
				label="Time"
				type="time"
				bind:value={() => `${pad(hour)}:${pad(minute)}`, setTime}
			/>
		{/if}
	</div>
	{#if preset === 'custom'}
		<Field label="Cron Expression" info={description} error={cronError}>
			{#snippet children(c)}
				<input
					id={c.id}
					class="dy-input mono"
					bind:value={cron}
					spellcheck="false"
					autocomplete="off"
					aria-describedby={[c.describedBy, previewId].filter(Boolean).join(' ')}
					aria-invalid={c.invalid || undefined}
				/>
			{/snippet}
		</Field>
	{:else if cronError}
		<p class="error" role="alert">{cronError}</p>
	{/if}
	<Combobox label="Time Zone" options={zones} bind:value={timeZone} />
	<div class="preview" id={previewId} aria-live="polite">
		{#if preview.data && !cronError}
			{#if preset === 'custom' && words !== cron.trim()}<p class="words">{words}</p>{/if}
			<p class="head">Next Runs</p>
			<ol role="list">
				{#each preview.data.runs as run (run.at)}
					<li class="num">
						{formatDateTime(run.at, preview.data.timeZone)}
						{#if run.dst !== 'none' && run.dstNote}<span class="dst">{run.dstNote}</span
							>{/if}
					</li>
				{/each}
			</ol>
			{#if preview.data.notes.length}
				<details class="notes">
					<summary>Missed Runs, Restarts and Daylight Saving Time</summary>
					{#each preview.data.notes as note (note)}<p class="note">{note}</p>{/each}
				</details>
			{/if}
		{:else if preview.isFetching}
			<p class="head">Checking the schedule…</p>
		{/if}
	</div>
</fieldset>

<style>
	.cron {
		display: grid;
		gap: var(--space-3);
		min-width: 0;
		margin: 0;
		padding: 0;
		border: 0;
	}

	legend {
		float: left;
		width: 100%;
		margin-bottom: calc(-1 * var(--space-1));
		padding: 0;
		color: var(--text-default);
		font-size: var(--text-body);
		line-height: var(--leading-body);
		font-weight: var(--weight-medium);
	}

	/* The preset fields side by side; Custom shows only the Repeats select. */
	.presets {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
		gap: var(--space-3);
	}

	.presets.custom {
		grid-template-columns: minmax(0, 240px);
	}

	.error {
		color: var(--danger);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.words {
		color: var(--text-strong);
		margin-bottom: var(--space-1);
	}

	.preview {
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-canvas);
		min-height: 44px;
	}

	.head {
		color: var(--text-muted);
		font-size: var(--text-caption);
		margin-bottom: 4px;
	}

	ol {
		display: grid;
		gap: 2px;
		margin: 0;
	}

	li {
		color: var(--text-default);
	}

	.dst {
		margin-left: var(--space-2);
		color: var(--warn);
		font-size: var(--text-caption);
	}

	.notes {
		margin-top: var(--space-2);
	}

	summary {
		width: fit-content;
		color: var(--accent-text);
		font-size: var(--text-caption);
		cursor: pointer;
		border-radius: var(--radius-sm);
	}

	summary:focus-visible {
		outline: var(--focus-ring);
		outline-offset: var(--focus-offset);
	}

	.note {
		margin-top: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}
</style>
