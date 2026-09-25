<script lang="ts">
	// Create or edit an update policy (#20): target (create only), which
	// services follow their tag, check and run schedules (#13, both off
	// until turned on), an optional update window and the health wait.
	// Saving never runs anything.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, unwrap } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Button,
		Card,
		Checkbox,
		CronField,
		Notice,
		RadioGroup,
		Select,
		Switch,
		TextField,
		toast
	} from '$lib/ui';
	import { containersQuery, ifMatch, stacksQuery } from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import ChoiceGrid from '$lib/features/common/ChoiceGrid.svelte';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import { defaultSchedule, scheduleDefaultsQuery } from '$lib/features/common/schedules';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import { DAY_OPTIONS, isClock, type UpdatePolicy } from './model';
	import { updateKeys } from './queries';

	let {
		policy,
		environmentId: initialEnv = null
	}: { policy?: UpdatePolicy; environmentId?: string | null } = $props();

	const qc = useQueryClient();
	// Form state is initialised once from the policy being edited: a live
	// refetch never overwrites what the user is typing.
	const p = untrack(() => policy);
	const editing = !!p;
	const envs = createQuery(() => environmentsQuery());
	const defaults = createQuery(() => scheduleDefaultsQuery());

	let name = $state(p?.name ?? '');
	let environmentId = $state(p?.environmentId ?? untrack(() => initialEnv) ?? '');
	let targetType = $state<'stack' | 'container'>(p?.target.type ?? 'stack');
	let targetId = $state(p?.target.id ?? '');
	let excluded = $state<string[]>(p?.excludeServices ?? []);
	let checkEnabled = $state(p?.checkSchedule?.enabled ?? false);
	let checkCron = $state(p?.checkSchedule?.cron ?? '');
	let checkZone = $state(p?.checkSchedule?.timeZone ?? '');
	let runEnabled = $state(p?.runSchedule?.enabled ?? false);
	let runCron = $state(p?.runSchedule?.cron ?? '');
	let runZone = $state(p?.runSchedule?.timeZone ?? '');
	let windowOn = $state(!!p?.window);
	let windowDays = $state<number[]>(p?.window?.days ?? []);
	let windowStart = $state(p?.window?.start ?? '01:00');
	let windowEnd = $state(p?.window?.end ?? '05:00');
	let waitTimeout = $state(String(p?.waitTimeoutSeconds ?? ''));
	let touched = $state(false);
	let busy = $state(false);
	let error = $state<unknown>(null);

	// New policies start from the instance defaults (or DockYard's suggestions).
	let seeded = false;
	$effect(() => {
		if (editing || seeded || defaults.isPending) return;
		seeded = true;
		const c = defaultSchedule('update_check', defaults.data);
		const r = defaultSchedule('update_run', defaults.data);
		checkCron = c.cron;
		checkZone = c.timeZone;
		runCron = r.cron;
		runZone = r.timeZone;
	});

	useUnsaved(
		() => `Update policy ${name || 'draft'}`,
		() => touched && !busy
	);

	$effect(() => {
		if (!environmentId && envs.data?.length === 1) environmentId = envs.data[0].id;
	});

	const stacks = createQuery(() => ({
		...stacksQuery(environmentId || null),
		enabled: !!environmentId
	}));
	const containers = createQuery(() => ({
		...containersQuery(environmentId),
		enabled: !!environmentId && targetType === 'container'
	}));
	const managedContainers = $derived(
		(containers.data ?? []).filter((c) => c.managed?.kind === 'standalone' && !c.stack)
	);
	const stack = $derived(stacks.data?.find((s) => s.id === targetId));
	const services = $derived(stack?.services ?? []);

	const envOptions = $derived(
		(envs.data ?? [])
			.filter((e) => e.status !== 'archived')
			.map((e) => ({ value: e.id, label: e.online ? e.name : `${e.name} (offline)` }))
	);
	const targetOptions = $derived(
		targetType === 'stack'
			? (stacks.data ?? []).map((s) => ({ value: s.id, label: s.displayName || s.name }))
			: managedContainers.map((c) => ({ value: c.name, label: c.name }))
	);

	const fields = $derived(fieldErrors(error));
	const windowError = $derived(
		windowOn && (!isClock(windowStart) || !isClock(windowEnd))
			? 'Use 24-hour times such as 01:00 and 05:30.'
			: null
	);

	function toggleExcluded(svc: string, follow: boolean) {
		touched = true;
		excluded = follow ? excluded.filter((s) => s !== svc) : [...excluded, svc];
	}

	function toggleDay(d: number, on: boolean) {
		touched = true;
		windowDays = on ? [...windowDays, d].sort() : windowDays.filter((x) => x !== d);
	}

	const canSave = $derived(
		name.trim().length > 0 &&
			(editing || (environmentId && targetId)) &&
			!windowError &&
			checkCron.trim() &&
			runCron.trim()
	);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		const wait = waitTimeout.trim() ? Number(waitTimeout) : undefined;
		const window = windowOn
			? {
					days: windowDays.length ? windowDays : undefined,
					start: windowStart,
					end: windowEnd
				}
			: undefined;
		try {
			let saved: UpdatePolicy;
			if (!policy) {
				saved = await unwrap(
					api.POST('/api/v1/update-policies', {
						body: {
							name: name.trim(),
							environmentId,
							target: { type: targetType, id: targetId },
							excludeServices: targetType === 'stack' ? excluded : undefined,
							checkSchedule: {
								cron: checkCron,
								timeZone: checkZone,
								enabled: checkEnabled
							},
							runSchedule: { cron: runCron, timeZone: runZone, enabled: runEnabled },
							window,
							waitTimeoutSeconds: wait
						}
					})
				);
				toast.success(`Created update policy ${saved.name}`, {
					body:
						checkEnabled || runEnabled
							? undefined
							: 'Its schedules are off: nothing runs until you turn them on.'
				});
			} else {
				saved = await unwrap(
					api.PATCH('/api/v1/update-policies/{policyId}', {
						params: {
							path: { policyId: policy.id },
							header: { 'If-Match': ifMatch(policy.revision) }
						},
						body: {
							name: name.trim(),
							excludeServices: policy.target.type === 'stack' ? excluded : undefined,
							checkSchedule: {
								cron: checkCron,
								timeZone: checkZone,
								enabled: checkEnabled
							},
							runSchedule: { cron: runCron, timeZone: runZone, enabled: runEnabled },
							window,
							clearWindow: windowOn ? undefined : true,
							waitTimeoutSeconds: wait
						}
					})
				);
				toast.success(`Saved update policy ${saved.name}`);
			}
			touched = false;
			await qc.invalidateQueries({ queryKey: ['policies'] });
			qc.setQueryData(updateKeys.detail(saved.id), saved);
			await goto(routes.updatePolicy(saved.id));
		} catch (err) {
			error = err;
		} finally {
			busy = false;
		}
	}
</script>

<form onsubmit={submit} oninput={() => (touched = true)} novalidate>
	<Page>
		{#if error && Object.keys(fields).length === 0}
			<Notice
				tone="danger"
				title={editing ? 'The policy was not saved' : 'The policy was not created'}
				live="alert"
			>
				{actionError(error, {
					update_policy_target_used:
						'This target already has an update policy. Edit that policy instead.',
					update_policy_name_taken:
						'Another update policy has this name. Choose a different name.',
					update_target_ineligible:
						"DockYard's own containers and stack can't be updated this way."
				})}
			</Notice>
		{/if}

		<Card title="Target">
			<Fields>
				<TextField
					label="Name"
					bind:value={name}
					required
					error={fields['body.name']}
					placeholder="Silo images"
				/>
				{#if editing && policy}
					<p class="muted">
						Follows the {policy.target.type === 'stack' ? 'stack' : 'container'}
						<strong class="strong"
							>{stack?.displayName || stack?.name || policy.target.id}</strong
						>. The target can't change; create another policy for a different one.
					</p>
				{:else}
					<Select
						label="Environment"
						options={envOptions}
						bind:value={environmentId}
						placeholder="Choose an environment"
						required
						error={fields['body.environmentId']}
						onchange={() => (targetId = '')}
					/>
					<RadioGroup
						label="What to update"
						bind:value={targetType}
						onchange={() => (targetId = '')}
						options={[
							{
								value: 'stack',
								label: 'A stack',
								description: 'Its services follow the tags in its Compose file.'
							},
							{
								value: 'container',
								label: 'A standalone container',
								description:
									'Only containers DockYard created and can recreate from their saved specification.'
							}
						]}
					/>
					{#if environmentId}
						<Select
							label={targetType === 'stack' ? 'Stack' : 'Container'}
							options={targetOptions}
							bind:value={targetId}
							placeholder={targetOptions.length
								? `Choose a ${targetType}`
								: targetType === 'stack'
									? 'No stacks in this environment'
									: 'No DockYard-managed containers here'}
							description={targetType === 'container'
								? "DockYard's own containers are never offered: DockYard is upgraded with its own images, not by an update policy."
								: "DockYard's own Compose project is not a stack and can't be updated this way."}
							required
							error={fields['body.target.id'] ?? fields['body.target']}
						/>
					{/if}
				{/if}
			</Fields>
		</Card>

		{#if (policy?.target.type ?? targetType) === 'stack' && services.length}
			<Card
				title="Services"
				subtitle="Checked services follow their tag. Services added later follow theirs too, unless you exclude them here."
			>
				<ChoiceGrid min="240px">
					{#each services as svc (svc.name)}
						<Checkbox
							label={svc.name}
							description={svc.build ? 'Built from source: not eligible' : svc.image}
							checked={!excluded.includes(svc.name)}
							onchange={(e) => toggleExcluded(svc.name, e.currentTarget.checked)}
						/>
					{/each}
				</ChoiceGrid>
			</Card>
		{/if}

		<Card
			title="Checks"
			subtitle="A check compares the digest behind each tag with what runs on the host. It never pulls or changes anything."
		>
			<Fields>
				<Switch
					label="Check automatically"
					description="Off: checks run only when you start them."
					bind:checked={checkEnabled}
					onchange={() => (touched = true)}
				/>
				{#if checkZone}
					<CronField
						label="Check schedule"
						kind="update_check"
						bind:cron={checkCron}
						bind:timeZone={checkZone}
					/>
				{/if}
			</Fields>
		</Card>

		<Card
			title="Automatic updates"
			subtitle="An update pulls the new image and recreates the services that changed, in dependency order."
		>
			<Fields>
				<Switch
					label="Update automatically"
					description="Off: updates run only when you apply them from the preview."
					bind:checked={runEnabled}
					onchange={() => (touched = true)}
				/>
				{#if runEnabled}
					<Notice
						tone="warn"
						icon={TriangleAlert}
						title="Containers restart without asking"
						live="none"
					>
						Scheduled updates recreate containers as soon as a new digest is found. A
						failed update is not rolled back: its digest is quarantined and you pin a
						working image yourself.
					</Notice>
				{/if}
				{#if runZone}
					<CronField
						label="Update schedule"
						kind="update_run"
						bind:cron={runCron}
						bind:timeZone={runZone}
					/>
				{/if}
				<Switch
					label="Only update inside a window"
					description="Scheduled updates wait for the window; manual updates run any time."
					bind:checked={windowOn}
					onchange={() => (touched = true)}
				/>
				{#if windowOn}
					<FieldGroup legend="Update window" hint="No day checked: every day.">
						<ChoiceGrid min="72px">
							{#each DAY_OPTIONS as d (d.value)}
								<Checkbox
									label={d.label}
									checked={windowDays.includes(d.value)}
									onchange={(e) => toggleDay(d.value, e.currentTarget.checked)}
								/>
							{/each}
						</ChoiceGrid>
						<Fields columns={2}>
							<TextField
								label="From"
								bind:value={windowStart}
								mono
								inputmode="numeric"
								placeholder="01:00"
								error={windowError}
							/>
							<TextField
								label="Until"
								bind:value={windowEnd}
								mono
								inputmode="numeric"
								placeholder="05:00"
								description="Before the start time: the window spans midnight."
							/>
						</Fields>
					</FieldGroup>
				{/if}
				<TextField
					label="Health wait"
					description="Optional. Seconds to wait for recreated containers to become healthy before the update counts as failed."
					bind:value={waitTimeout}
					inputmode="numeric"
					error={fields['body.waitTimeoutSeconds']}
				/>
			</Fields>
		</Card>
	</Page>

	<FormFooter>
		<Button href={policy ? routes.updatePolicy(policy.id) : routes.updates()} variant="ghost"
			>Cancel</Button
		>
		<Button type="submit" variant="primary" loading={busy} disabled={!canSave}>
			{editing ? 'Save changes' : 'Create update policy'}
		</Button>
	</FormFooter>
</form>

<style>
	.strong {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}
</style>
