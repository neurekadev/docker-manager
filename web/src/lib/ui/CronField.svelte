<script lang="ts">
	// Cron field (#13): five-field expression + IANA time zone, with the next
	// runs previewed by the manager (POST /schedules/previews, the one cron
	// parser, #13) so DST gaps and repeats are explained exactly as they will
	// run. The preview is debounced; server validation errors show inline.
	import { createQuery } from '@tanstack/svelte-query';
	import { ApiRequestError } from '$lib/api/client';
	import { schedulePreviewQuery } from '$lib/api/queries';
	import Combobox from './Combobox.svelte';
	import Field from './Field.svelte';
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

<div class="cron">
	<Field {label} {description} error={cronError}>
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
	<Combobox label="Time zone" options={zones} bind:value={timeZone} />
	<div class="preview" id={previewId} aria-live="polite">
		{#if preview.data && !cronError}
			<p class="head">Next runs</p>
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
					<summary>Missed runs, restarts and daylight saving time</summary>
					{#each preview.data.notes as note (note)}<p class="note">{note}</p>{/each}
				</details>
			{/if}
		{:else if preview.isFetching}
			<p class="head">Checking the schedule…</p>
		{/if}
	</div>
</div>

<style>
	.cron {
		display: grid;
		gap: var(--space-3);
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
