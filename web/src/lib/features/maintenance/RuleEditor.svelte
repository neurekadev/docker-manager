<script lang="ts">
	// One prune rule (#14): on/off, age threshold, the category's own
	// options and filters. Volume rules show what they destroy and need
	// their own explicit opt-in before they can be turned on. The Engine's
	// limitations of the category are listed, never silently widened.
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import {
		Badge,
		Checkbox,
		Notice,
		Select,
		Switch,
		TextArea,
		TextField,
		formatNumber
	} from '$lib/ui';
	import ChoiceGrid from '$lib/features/common/ChoiceGrid.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import {
		ageText,
		categoryLabel,
		isVolumeCategory,
		joinAge,
		linesToList,
		listToLines,
		ruleProblem,
		splitAge,
		type CategoryInfo,
		type MaintenanceRule
	} from './model';

	interface Props {
		rule: MaintenanceRule;
		info?: CategoryInfo[];
		/** The instance default of this category (shown as the safe default). */
		suggested?: MaintenanceRule;
		disabled?: boolean;
		onchange: (rule: MaintenanceRule) => void;
	}

	let { rule, info, suggested, disabled = false, onchange }: Props = $props();

	const meta = $derived(info?.find((i) => i.category === rule.category));
	const label = $derived(categoryLabel(rule.category, info));
	const volume = $derived(isVolumeCategory(rule.category));
	const age = $derived(splitAge(rule.minAgeHours));
	const problem = $derived(ruleProblem(rule));
	let open = $state(false);

	function set(patch: Partial<MaintenanceRule>) {
		onchange({ ...rule, ...patch });
	}

	function setAge(value: string, unit: 'days' | 'hours') {
		const n = Number(value);
		set({ minAgeHours: Number.isFinite(n) ? Math.round(joinAge(n, unit)) : -1 });
	}

	const STATES = [
		{ value: 'exited', label: 'Exited' },
		{ value: 'dead', label: 'Dead' },
		{ value: 'created', label: 'Created, never started' }
	];
	const states = $derived(
		rule.containerStates?.length ? rule.containerStates : ['exited', 'dead']
	);
</script>

<section class="rule" class:on={rule.enabled} aria-label={label}>
	<div class="head">
		<Switch
			{label}
			description={meta?.description}
			checked={rule.enabled}
			disabled={disabled || (volume && !rule.volumeOptIn && !rule.enabled)}
			onchange={(v) => set({ enabled: v })}
		/>
		<div class="badges">
			{#if meta?.deletesData || volume}<Badge tone="danger">Deletes data</Badge>{/if}
			{#if rule.enabled}<Badge tone="warn" dot>{ageText(rule.minAgeHours)}</Badge>{/if}
		</div>
		<button type="button" class="toggle" aria-expanded={open} onclick={() => (open = !open)}>
			{open ? 'Hide options' : 'Options'}
		</button>
	</div>

	{#if volume}
		<div class="optin">
			<Checkbox
				label="I understand that removing volumes deletes the data in them"
				description="Needed before this rule can be turned on. Volumes of Docker Manager stacks, saved containers and backups are always kept."
				checked={!!rule.volumeOptIn}
				{disabled}
				onchange={(e) =>
					set({
						volumeOptIn: e.currentTarget.checked,
						enabled: e.currentTarget.checked ? rule.enabled : false
					})}
			/>
		</div>
	{/if}

	{#if problem}
		<p class="problem" role="alert">{problem}</p>
	{/if}

	{#if open}
		<div class="options">
			<Fields columns={2}>
				<TextField
					label="Only remove objects older than"
					type="number"
					min="0"
					value={String(age.value)}
					{disabled}
					onchange={(e) => setAge(e.currentTarget.value, age.unit)}
					description={suggested
						? `Default: ${ageText(suggested.minAgeHours)}. 0 removes objects of any age.`
						: '0 removes objects of any age.'}
				/>
				<Select
					label="Unit"
					options={[
						{ value: 'days', label: 'Days' },
						{ value: 'hours', label: 'Hours' }
					]}
					value={age.unit}
					{disabled}
					onchange={(v) => setAge(String(age.value), v as 'days' | 'hours')}
				/>
			</Fields>

			{#if rule.category === 'stopped_containers'}
				<fieldset class="states">
					<legend>Container states</legend>
					<ChoiceGrid min="160px">
						{#each STATES as s (s.value)}
							<Checkbox
								label={s.label}
								checked={states.includes(s.value)}
								{disabled}
								onchange={(e) =>
									set({
										containerStates: e.currentTarget.checked
											? [...states, s.value]
											: states.filter((x) => x !== s.value)
									})}
							/>
						{/each}
					</ChoiceGrid>
				</fieldset>
			{/if}

			{#if rule.category === 'build_cache'}
				<Fields columns={2}>
					<Switch
						label="All unused records"
						description="Off: only dangling records (not shared, not internal)."
						checked={!!rule.buildCacheAll}
						{disabled}
						onchange={(v) => set({ buildCacheAll: v })}
					/>
					<TextField
						label="Keep the most recent cache up to (GB)"
						type="number"
						min="0"
						step="0.5"
						value={rule.keepStorageBytes
							? formatNumber(rule.keepStorageBytes / 1024 ** 3)
							: ''}
						description="Optional. Empty: no cap."
						{disabled}
						onchange={(e) =>
							set({
								keepStorageBytes: e.currentTarget.value
									? Math.round(Number(e.currentTarget.value) * 1024 ** 3)
									: undefined
							})}
					/>
				</Fields>
			{/if}

			{#if meta?.labels !== false && rule.category !== 'build_cache'}
				<Fields columns={2}>
					<TextArea
						label="Only objects with these labels"
						description="Optional. One per line, key or key=value; all must match."
						value={listToLines(rule.includeLabels)}
						mono
						rows={2}
						{disabled}
						onchange={(e) => set({ includeLabels: linesToList(e.currentTarget.value) })}
					/>
					<TextArea
						label="Never objects with these labels"
						description="Optional. One per line; any match keeps the object."
						value={listToLines(rule.excludeLabels)}
						mono
						rows={2}
						{disabled}
						onchange={(e) => set({ excludeLabels: linesToList(e.currentTarget.value) })}
					/>
				</Fields>
			{/if}
			<TextArea
				label="Never remove"
				description="Optional. Names or IDs (at least 12 characters), one per line."
				value={listToLines(rule.exclude)}
				mono
				rows={2}
				{disabled}
				onchange={(e) => set({ exclude: linesToList(e.currentTarget.value) })}
			/>
			{#if meta?.limitations.length}
				<Notice
					tone="info"
					icon={TriangleAlert}
					title="What the Engine can't do here"
					live="none"
				>
					<ul class="limits" role="list">
						{#each meta.limitations as l (l)}<li>{l}</li>{/each}
					</ul>
				</Notice>
			{/if}
		</div>
	{/if}
</section>

<style>
	.rule {
		display: grid;
		gap: var(--space-3);
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-panel);
	}

	.rule.on {
		border-color: var(--border-strong);
		background: var(--surface-raised);
	}

	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-start;
		gap: var(--space-3);
	}

	.head > :global(:first-child) {
		flex: 1 1 280px;
	}

	.badges {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.toggle {
		padding: 2px var(--space-2);
		border: 0;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--accent-text);
		font-size: var(--text-caption);
		cursor: pointer;
	}

	.toggle:hover {
		background: var(--surface-hover);
	}

	.optin {
		padding: var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
	}

	.problem {
		color: var(--danger);
		font-size: var(--text-caption);
	}

	.options {
		display: grid;
		gap: var(--space-4);
		padding-top: var(--space-3);
		border-top: 1px solid var(--border-subtle);
	}

	.states {
		margin: 0;
		padding: 0;
		border: 0;
		min-width: 0;
	}

	legend {
		margin-bottom: var(--space-2);
		color: var(--text-default);
		font-weight: var(--weight-medium);
	}

	.limits {
		display: grid;
		gap: 2px;
	}

	@media (pointer: coarse) {
		.toggle {
			min-height: var(--touch-target);
		}
	}
</style>
