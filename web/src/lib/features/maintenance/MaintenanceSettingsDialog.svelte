<script lang="ts">
	// Edit the maintenance settings (#14, #238) in a dialog: whether it runs
	// on its schedule, the environments it covers (every one unless left
	// out, also ones added later), then one rule per category (every rule
	// starts off, 30 days; volume rules need their own opt-in). Saving never
	// removes anything; preview on the Maintenance page. Render it only while
	// open ({#if}): each opening starts from `settings`.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { Badge, Button, CronField, Dialog, Notice, Skeleton, Switch, toast } from '$lib/ui';
	import CoverageList from '$lib/features/common/CoverageList.svelte';
	import { ifMatch } from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import RuleList from './RuleList.svelte';
	import {
		normalizeRules,
		ruleProblem,
		type MaintenanceRule,
		type MaintenanceSettings
	} from './model';
	import { maintenanceKeys } from './queries';

	let {
		open = $bindable(true),
		settings
	}: {
		open?: boolean;
		settings: MaintenanceSettings;
	} = $props();

	const qc = useQueryClient();
	const st = untrack(() => settings);
	const envs = createQuery(() => environmentsQuery());

	let rules = $state<MaintenanceRule[]>(normalizeRules(st.rules));
	let enabled = $state(st.enabled);
	let cron = $state(st.schedule.cron);
	let zone = $state(st.schedule.timeZone);
	let excluded = $state<string[]>([...st.excludeEnvironments]);
	let touched = $state(false);
	let busy = $state(false);
	let error = $state<unknown>(null);

	useUnsaved(
		() => 'Maintenance settings',
		() => touched && !busy
	);

	const environments = $derived(
		(envs.data ?? [])
			.filter((e) => e.status !== 'archived')
			.map((e) => ({
				key: e.id,
				label: e.name,
				description: e.online ? undefined : 'Offline'
			}))
	);
	const fields = $derived(fieldErrors(error));
	const problems = $derived(rules.map(ruleProblem).filter(Boolean));
	const on = $derived(rules.filter((r) => r.enabled).length);
	const canSave = $derived(problems.length === 0 && !!cron.trim());

	function setRule(i: number, r: MaintenanceRule) {
		touched = true;
		rules[i] = r;
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (!canSave) return;
		busy = true;
		error = null;
		try {
			const saved = await unwrap(
				api.PATCH('/api/v1/maintenance-settings', {
					params: { header: { 'If-Match': ifMatch(st.revision) } },
					body: {
						enabled,
						schedule: { cron, timeZone: zone },
						rules,
						excludeEnvironments: excluded
					}
				})
			);
			toast.success('Saved the maintenance settings', {
				body: saved.enabled
					? undefined
					: 'Maintenance is off: nothing is removed until you run it or turn it on.'
			});
			touched = false;
			qc.setQueryData(maintenanceKeys.settings, saved);
			await qc.invalidateQueries({ queryKey: ['policies'] });
			open = false;
		} catch (err) {
			error = err;
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title="Edit Maintenance"
	description="Docker Manager's own objects, stack resources, saved containers and backups are always kept."
	size="xl"
	dismissible={!busy}
>
	<form id="maintenance-form" onsubmit={submit} oninput={() => (touched = true)} novalidate>
		{#if error && Object.keys(fields).length === 0}
			<div class="error">
				<Notice tone="danger" title="The settings were not saved" live="alert">
					{actionError(error)}
				</Notice>
			</div>
		{/if}
		<div class="top">
			<Fields>
				<FieldGroup
					legend="Environments"
					hint="Uncheck an environment to leave it out. Environments added later are covered."
				>
					{#if envs.isPending}
						<Skeleton lines={2} height="20px" />
					{:else if !environments.length}
						<p class="muted">No environments yet.</p>
					{:else}
						<CoverageList
							label="Environments Covered"
							min="180px"
							items={environments}
							{excluded}
							onchange={(v) => {
								excluded = v;
								touched = true;
							}}
						/>
					{/if}
				</FieldGroup>
			</Fields>
			<FieldGroup
				legend="Schedule"
				info="Runs missed while Docker Manager was down are skipped, never run late."
			>
				<Switch label="Enabled" bind:checked={enabled} onchange={() => (touched = true)} />
				{#if enabled}
					<CronField label="Schedule" kind="prune" bind:cron bind:timeZone={zone} />
				{/if}
			</FieldGroup>
		</div>

		<div class="rules-head">
			<h3 class="section">Rules</h3>
			<Badge tone={on ? 'accent' : 'neutral'}>{on} of {rules.length} on</Badge>
		</div>
		<RuleList
			{rules}
			columns={2}
			info={st.categories}
			suggested={st.suggestedRules}
			onchange={setRule}
		/>
	</form>
	{#snippet footer()}
		<Button variant="ghost" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
		<Button
			type="submit"
			form="maintenance-form"
			variant="primary"
			loading={busy}
			disabled={!canSave}
		>
			Save Changes
		</Button>
	{/snippet}
</Dialog>

<style>
	.top {
		display: grid;
		grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
		gap: var(--space-5);
		align-items: stretch;
	}

	.rules-head {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		margin: var(--space-6) 0 var(--space-3);
	}

	.section {
		color: var(--text-strong);
		font-size: var(--text-control);
		font-weight: var(--weight-semibold);
	}

	.error {
		margin-bottom: var(--space-4);
	}

	@media (max-width: 1023px) {
		.top {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
