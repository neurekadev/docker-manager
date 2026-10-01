<script lang="ts">
	// Alert thresholds (Settings → Notifications, owner only): the warning
	// and critical levels of temperature, disk space and memory alerts for
	// every environment, checked in words as they are typed ("Save
	// thresholds" only while something changed and everything is right),
	// and below them each environment's override (Edit, Remove; "Add
	// override" opens OverrideDialog). Every change replaces the whole
	// settings with If-Match; there is no live event, the response is the
	// new state.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Plus from '@lucide/svelte/icons/plus';
	import { environmentsQuery } from '$lib/api/queries';
	import { actionError } from '$lib/features/common/errors';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import {
		Button,
		Card,
		ConfirmDialog,
		IconButton,
		Menu,
		Table,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import OverrideDialog from './OverrideDialog.svelte';
	import { alertSettingsKey, alertSettingsQuery, saveAlertSettings } from './queries';
	import ThresholdGrid from './ThresholdGrid.svelte';
	import {
		THRESHOLD_METRICS,
		brokenOverrides,
		checkThresholds,
		noErrors,
		overrideLevels,
		overrideTitle,
		sameForm,
		serverThresholdErrors,
		settingsBody,
		thresholdForm,
		thresholdsOf,
		type AlertSettings,
		type ThresholdForm,
		type ThresholdMetric,
		type ThresholdOverride
	} from './thresholds';

	const queryClient = useQueryClient();
	const settings = createQuery(() => alertSettingsQuery());
	const envs = createQuery(() => environmentsQuery());
	const environments = $derived(envs.data ?? []);
	const nameOf = (id: string) =>
		environments.find((e) => e.id === id)?.name ?? 'Removed environment';

	/** The levels as typed, and as saved (to see what changed). */
	let form = $state<ThresholdForm | null>(null);
	let base = $state<ThresholdForm | null>(null);
	$effect(() => {
		const d = settings.data;
		if (!d) return;
		untrack(() => {
			const next = thresholdForm(d.thresholds);
			// Keep what the owner is typing; follow the saved levels otherwise.
			if (!form || !base || sameForm(form, base)) form = next;
			base = next;
		});
	});

	let saving = $state(false);
	let failure = $state<unknown>(null);
	const errors = $derived(
		form ? { ...checkThresholds(form), ...serverThresholdErrors(failure) } : {}
	);
	const changed = $derived(!!form && !!base && !sameForm(form, base));
	/** Overrides the new defaults would make wrong (the manager refuses them). */
	const broken = $derived(
		form && settings.data && noErrors(checkThresholds(form))
			? brokenOverrides(settings.data.overrides, thresholdsOf(form))
			: []
	);
	const canSave = $derived(changed && noErrors(errors) && broken.length === 0);

	async function save(d: AlertSettings) {
		if (!form || !canSave) return;
		saving = true;
		failure = null;
		try {
			const saved = await saveAlertSettings(d, settingsBody(thresholdsOf(form), d.overrides));
			form = thresholdForm(saved.thresholds);
			base = form;
			queryClient.setQueryData(alertSettingsKey, saved);
			toast.success('Saved alert thresholds');
		} catch (e) {
			failure = e;
			if (noErrors(serverThresholdErrors(e)))
				toast.error('The alert thresholds could not be saved', { body: actionError(e) });
			void queryClient.invalidateQueries({ queryKey: alertSettingsKey });
		} finally {
			saving = false;
		}
	}

	let dialogOpen = $state(false);
	let editing = $state<string | null>(null);
	let removing = $state<ThresholdOverride | null>(null);
	let removeOpen = $state(false);

	function add() {
		editing = null;
		dialogOpen = true;
	}

	async function remove(d: AlertSettings) {
		const o = removing!;
		const saved = await saveAlertSettings(
			d,
			settingsBody(
				d.thresholds,
				d.overrides.filter((x) => x.environmentId !== o.environmentId)
			)
		);
		queryClient.setQueryData(alertSettingsKey, saved);
		toast.success(`Removed the override of ${nameOf(o.environmentId)}`);
	}

	function menu(o: ThresholdOverride): MenuEntry[] {
		return [
			{
				label: 'Edit',
				onSelect: () => {
					editing = o.environmentId;
					dialogOpen = true;
				}
			},
			{ separator: true },
			{
				label: 'Remove',
				tone: 'danger',
				onSelect: () => {
					removing = o;
					removeOpen = true;
				}
			}
		];
	}

	/** Active environments without an override (what "Add override" can choose). */
	const addable = $derived(
		environments.filter(
			(e) =>
				!e.archivedAt &&
				!(settings.data?.overrides ?? []).some((o) => o.environmentId === e.id)
		).length
	);

	// One cell per metric (a column's cell takes only the row).
	const LEVEL_CELLS = {
		temperature: temperatureCell,
		diskSpace: diskSpaceCell,
		memory: memoryCell
	};

	const columns = $derived.by(() => {
		const d = settings.data;
		const cols: Column<ThresholdOverride>[] = [
			{
				id: 'environment',
				header: 'Environment',
				cell: envCell,
				sortValue: (o) => nameOf(o.environmentId),
				maxWidth: '220px',
				truncate: true,
				title: (o) => nameOf(o.environmentId),
				stack: 'title'
			},
			...THRESHOLD_METRICS.map((m): Column<ThresholdOverride> => ({
				id: m.metric,
				header: m.short,
				cell: LEVEL_CELLS[m.metric],
				title: (o) => (d ? overrideTitle(o, m, d.thresholds) : ''),
				stack: 'meta'
			})),
			{
				id: 'actions',
				header: 'Actions',
				hideHeader: true,
				cell: actionsCell,
				width: '56px',
				align: 'end',
				stack: 'head'
			}
		];
		return cols;
	});
</script>

{#snippet envCell(o: ThresholdOverride)}<span class="name">{nameOf(o.environmentId)}</span
	>{/snippet}
{#snippet levels(o: ThresholdOverride, m: ThresholdMetric)}
	{@const text = overrideLevels(o, m)}
	<span class:muted={text === 'Default'}><span class="metric">{m.short}: </span>{text}</span>
{/snippet}
{#snippet temperatureCell(o: ThresholdOverride)}{@render levels(o, THRESHOLD_METRICS[0])}{/snippet}
{#snippet diskSpaceCell(o: ThresholdOverride)}{@render levels(o, THRESHOLD_METRICS[1])}{/snippet}
{#snippet memoryCell(o: ThresholdOverride)}{@render levels(o, THRESHOLD_METRICS[2])}{/snippet}
{#snippet actionsCell(o: ThresholdOverride)}
	<Menu items={menu(o)} label="Actions for {nameOf(o.environmentId)}" align="end">
		{#snippet trigger(props)}
			<IconButton
				{...props}
				icon={Ellipsis}
				label="Actions for {nameOf(o.environmentId)}"
				size="sm"
			/>
		{/snippet}
	</Menu>
{/snippet}

<Card
	title="Alert thresholds"
	id="alert-thresholds"
	subtitle="Alerts when a host runs hot or low on disk space or memory for 5 minutes. 0 turns a level off."
>
	<QueryView query={settings} errorTitle="The alert thresholds could not be loaded.">
		{#snippet children(d)}
			<form
				class="defaults"
				novalidate
				onsubmit={(e) => {
					e.preventDefault();
					void save(d);
				}}
			>
				{#if form}
					<ThresholdGrid
						label="Default levels"
						{form}
						{errors}
						onchange={(k, v) => {
							form = { ...form!, [k]: v };
							failure = null;
						}}
					/>
				{/if}
				{#if broken.length}
					<p class="problem" role="alert">
						Change or remove the override of {broken.map(nameOf).join(', ')} first: with these
						levels its warning level would not be below its critical one.
					</p>
				{/if}
				<div class="save">
					<Button type="submit" variant="primary" disabled={!canSave} loading={saving}
						>Save thresholds</Button
					>
				</div>
			</form>

			<section class="overrides" aria-labelledby="threshold-overrides">
				<div class="overrides-head">
					<h3 id="threshold-overrides" class="subsection-title">Environment overrides</h3>
					<Button
						size="sm"
						icon={Plus}
						disabled={addable === 0}
						title={addable === 0 ? 'Every environment has an override.' : undefined}
						onclick={add}>Add override</Button
					>
				</div>
				{#if d.overrides.length}
					<div class="table">
						<Table
							label="Environment overrides"
							rows={d.overrides}
							{columns}
							rowKey={(o) => o.environmentId}
							sort={{ column: 'environment', direction: 'asc' }}
						/>
					</div>
				{:else}
					<p class="muted">No overrides: every environment uses the levels above.</p>
				{/if}
			</section>

			<OverrideDialog
				bind:open={dialogOpen}
				settings={d}
				environmentId={editing}
				{environments}
			/>
			{#if removing}
				<ConfirmDialog
					bind:open={removeOpen}
					title="Remove the override of {nameOf(removing.environmentId)}?"
					message="{nameOf(removing.environmentId)} uses the default levels again."
					confirmLabel="Remove override"
					tone="danger"
					onconfirm={() => remove(d)}
				/>
			{/if}
		{/snippet}
	</QueryView>
</Card>

<style>
	.defaults {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}

	.save {
		display: flex;
	}

	.problem {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}

	.overrides {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		margin-top: var(--space-5);
		padding-top: var(--space-5);
		border-top: 1px solid var(--border-subtle);
	}

	.overrides-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}

	.table {
		overflow: hidden;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.muted {
		color: var(--text-muted);
	}

	/* The metric's name repeats only in the stacked phone cards. */
	.metric {
		display: none;
	}

	@media (max-width: 767px) {
		.metric {
			display: inline;
		}
	}
</style>
