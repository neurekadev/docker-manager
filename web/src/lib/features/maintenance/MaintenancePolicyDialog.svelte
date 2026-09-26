<script lang="ts">
	// Create or edit a prune policy (#14) in a dialog: name, scope and
	// schedule on top (the schedule stays off until turned on), then one rule
	// per category starting from the instance's maintenance defaults (every
	// rule off, 30 days, volume rules need their own opt-in). Saving never
	// removes anything; preview on the policy page. Render it only while
	// open ({#if}): each opening starts from `policy`.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { api, unwrap } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		CronField,
		Dialog,
		Notice,
		Select,
		Switch,
		TextField,
		toast
	} from '$lib/ui';
	import { ifMatch } from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import { defaultSchedule, scheduleDefaultsQuery } from '$lib/features/common/schedules';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import RuleList from './RuleList.svelte';
	import {
		normalizeRules,
		ruleProblem,
		type MaintenancePolicy,
		type MaintenanceRule
	} from './model';
	import { maintenanceDefaultsQuery, maintenanceKeys } from './queries';

	let {
		open = $bindable(true),
		policy,
		environmentId: initialEnv = null,
		allowAll = true
	}: {
		open?: boolean;
		policy?: MaintenancePolicy;
		environmentId?: string | null;
		allowAll?: boolean;
	} = $props();

	const qc = useQueryClient();
	const p = untrack(() => policy);
	const editing = !!p;
	const envs = createQuery(() => environmentsQuery());
	const defaults = createQuery(() => maintenanceDefaultsQuery());
	const schedDefaults = createQuery(() => scheduleDefaultsQuery());

	let name = $state(p?.name ?? '');
	let description = $state(p?.description ?? '');
	let environmentId = $state(p?.environmentId ?? untrack(() => initialEnv) ?? '');
	let rules = $state<MaintenanceRule[]>(normalizeRules(p?.rules));
	let enabled = $state(p?.schedule?.enabled ?? false);
	let cron = $state(p?.schedule?.cron ?? '');
	let zone = $state(p?.schedule?.timeZone ?? '');
	let touched = $state(false);
	let busy = $state(false);
	let error = $state<unknown>(null);
	$effect(() => {
		if (!editing && !allowAll && !environmentId) {
			environmentId = (envs.data ?? []).find((e) => e.status !== 'archived')?.id ?? '';
		}
	});

	// A new policy starts with the instance defaults.
	let seeded = false;
	$effect(() => {
		if (editing || seeded || defaults.isPending || schedDefaults.isPending) return;
		seeded = true;
		if (defaults.data) rules = normalizeRules(defaults.data.rules);
		const d = defaultSchedule('prune', schedDefaults.data);
		cron = d.cron;
		zone = d.timeZone;
	});

	useUnsaved(
		() => `Maintenance policy ${name || 'draft'}`,
		() => touched && !busy
	);

	const envOptions = $derived([
		...(allowAll || p?.environmentId === '' ? [{ value: '', label: 'All environments' }] : []),
		...(envs.data ?? [])
			.filter((e) => e.status !== 'archived')
			.map((e) => ({ value: e.id, label: e.online ? e.name : `${e.name} (offline)` }))
	]);
	const fields = $derived(fieldErrors(error));
	const problems = $derived(rules.map(ruleProblem).filter(Boolean));
	const on = $derived(rules.filter((r) => r.enabled).length);
	const canSave = $derived(!!name.trim() && problems.length === 0 && !!cron.trim());

	// Without settings.read the defaults are unknown here: send only the
	// rules the user changed and let the server start the rest from them.
	const changed: string[] = [];
	function setRule(i: number, r: MaintenanceRule) {
		touched = true;
		if (!changed.includes(r.category)) changed.push(r.category);
		rules[i] = r;
	}
	const createRules = () =>
		defaults.data ? rules : rules.filter((r) => changed.includes(r.category));

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (!canSave) return;
		busy = true;
		error = null;
		try {
			let saved: MaintenancePolicy;
			const schedule = { cron, timeZone: zone, enabled };
			if (!p) {
				saved = await unwrap(
					api.POST('/api/v1/maintenance-policies', {
						body: {
							name: name.trim(),
							description: description.trim() || undefined,
							environmentId,
							scope: environmentId ? 'environment' : 'all',
							rules: createRules(),
							schedule
						}
					})
				);
				toast.success(`Created maintenance policy ${saved.name}`, {
					body: enabled
						? undefined
						: 'Its schedule is off: nothing is removed until you run it or turn the schedule on.'
				});
			} else {
				saved = await unwrap(
					api.PATCH('/api/v1/maintenance-policies/{policyId}', {
						params: {
							path: { policyId: p.id },
							header: { 'If-Match': ifMatch(p.revision) }
						},
						body: {
							name: name.trim(),
							description: description.trim(),
							rules,
							schedule
						}
					})
				);
				toast.success(`Saved maintenance policy ${saved.name}`);
			}
			touched = false;
			await qc.invalidateQueries({ queryKey: ['policies'] });
			qc.setQueryData(maintenanceKeys.detail(saved.id), saved);
			// A new policy opens its page (the dialog goes with this one).
			if (p) open = false;
			else await goto(routes.maintenancePolicy(saved.id));
		} catch (err) {
			error = err;
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title={editing ? `Edit ${p?.name}` : 'Create maintenance policy'}
	description="Turn on the rules you want. Docker Manager's own objects, stack resources, saved containers and backups are always kept."
	size="xl"
	dismissible={!busy}
>
	<form
		id="maintenance-policy-form"
		onsubmit={submit}
		oninput={() => (touched = true)}
		novalidate
	>
		{#if error && Object.keys(fields).length === 0}
			<div class="error">
				<Notice
					tone="danger"
					title={editing ? 'The policy was not saved' : 'The policy was not created'}
					live="alert"
				>
					{actionError(error, {
						maintenance_policy_name_taken:
							'Another maintenance policy has this name. Choose a different name.'
					})}
				</Notice>
			</div>
		{/if}
		<div class="top">
			<Fields>
				<Fields columns={2}>
					<TextField
						label="Name"
						bind:value={name}
						required
						error={fields['body.name']}
						placeholder="Weekly cleanup"
					/>
					{#if editing}
						<div class="scope">
							<span class="scope-label">Environments</span>
							<span
								>{environmentId
									? (envs.data?.find((e) => e.id === environmentId)?.name ??
										'One environment')
									: 'All environments'}</span
							>
							<span class="muted small">Create another policy to change scope.</span>
						</div>
					{:else}
						<Select
							label="Environments"
							options={envOptions}
							bind:value={environmentId}
							placeholder="All environments"
							error={fields['body.environmentId']}
						/>
					{/if}
				</Fields>
				<TextField
					label="Description"
					description="Optional."
					bind:value={description}
					error={fields['body.description']}
				/>
			</Fields>
			<FieldGroup
				legend="Schedule"
				hint="Scheduled runs always run in the background. Runs missed while Docker Manager was down are skipped, never run late."
			>
				<Switch
					label="Run automatically"
					description="Off: the policy runs only when you start it."
					bind:checked={enabled}
					onchange={() => (touched = true)}
				/>
				{#if zone}
					<CronField label="Schedule" kind="prune" bind:cron bind:timeZone={zone} />
				{/if}
			</FieldGroup>
		</div>

		<div class="rules-head">
			<h3 class="section">Rules</h3>
			<Badge tone={on ? 'accent' : 'neutral'}>{on} of {rules.length} on</Badge>
		</div>
		<p class="muted small rules-hint">
			The rules turned on together are this policy's cleanup, from the least to the most
			destructive.
		</p>
		<RuleList
			{rules}
			columns={2}
			info={defaults.data?.categories}
			suggested={defaults.data?.rules}
			onchange={setRule}
		/>
	</form>
	{#snippet footer()}
		<Button variant="ghost" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
		<Button
			type="submit"
			form="maintenance-policy-form"
			variant="primary"
			loading={busy}
			disabled={!canSave}
		>
			{editing ? 'Save changes' : 'Create maintenance policy'}
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

	.scope {
		display: grid;
		gap: 2px;
	}

	.scope-label {
		color: var(--text-default);
		font-weight: var(--weight-medium);
	}

	.rules-head {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		margin-top: var(--space-6);
	}

	.rules-hint {
		margin: var(--space-1) 0 var(--space-3);
	}

	.section {
		color: var(--text-strong);
		font-size: var(--text-control);
		font-weight: var(--weight-semibold);
	}

	.error {
		margin-bottom: var(--space-4);
	}

	.small {
		font-size: var(--text-caption);
	}

	@media (max-width: 1023px) {
		.top {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
