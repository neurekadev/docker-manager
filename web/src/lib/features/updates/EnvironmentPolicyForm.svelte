<script lang="ts">
	// Create or edit an environment update policy (#20): scope (all
	// environments or one, fixed after creation), excluded stacks and
	// standalone containers, check and run schedules (#13, both off until
	// turned on), an optional update window and the health wait. Saving
	// never runs anything.
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
		Skeleton,
		Switch,
		TextField,
		toast
	} from '$lib/ui';
	import ChoiceGrid from '$lib/features/common/ChoiceGrid.svelte';
	import {
		environmentName,
		fetchAllPages,
		ifMatch,
		stacksQuery
	} from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import { defaultSchedule, scheduleDefaultsQuery } from '$lib/features/common/schedules';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import { DAY_OPTIONS, isClock } from './model';
	import { environmentUpdateKeys, type EnvironmentUpdatePolicy } from './queries';

	let {
		policy: initial,
		environmentId: suggestedEnvironment,
		allowAll = true
	}: {
		policy?: EnvironmentUpdatePolicy;
		environmentId?: string | null;
		allowAll?: boolean;
	} = $props();

	const qc = useQueryClient();
	// Form state is initialised once from the policy being edited: a live
	// refetch never overwrites what the user is typing.
	const p = untrack(() => initial);
	const editing = !!p;
	const initialEnvironment = untrack(() => suggestedEnvironment);
	const initialAllowAll = untrack(() => allowAll);
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());
	const defaults = createQuery(() => scheduleDefaultsQuery());

	let scope = $state<'all' | 'environment'>(
		p?.scope ?? (initialEnvironment || !initialAllowAll ? 'environment' : 'all')
	);
	let environmentId = $state(p?.environmentId ?? initialEnvironment ?? '');
	let name = $state(p?.name ?? '');
	let excludedStacks = $state<string[]>(p?.excludeStacks ?? []);
	let excludedContainers = $state<string[]>(p?.excludeContainers ?? []);
	let checkCron = $state(p?.checkSchedule.cron ?? '');
	let checkZone = $state(p?.checkSchedule.timeZone ?? '');
	let checkEnabled = $state(p?.checkSchedule.enabled ?? false);
	let runCron = $state(p?.runSchedule.cron ?? '');
	let runZone = $state(p?.runSchedule.timeZone ?? '');
	let runEnabled = $state(p?.runSchedule.enabled ?? false);
	let windowOn = $state(!!p?.window);
	let windowStart = $state(p?.window?.start ?? '02:00');
	let windowEnd = $state(p?.window?.end ?? '05:00');
	let windowDays = $state<number[]>(p?.window?.days ?? []);
	let waitTimeout = $state(p?.waitTimeoutSeconds ? String(p.waitTimeoutSeconds) : '');
	let busy = $state(false);
	let touched = $state(false);
	let error = $state<unknown>(null);
	let containers = $state<{ key: string; name: string; environment: string }[]>([]);
	let containersLoading = $state(false);
	let containerError = $state(false);
	let loadVersion = 0;

	// New policies start from the instance defaults (or Docker Manager's suggestions).
	let seeded = false;
	$effect(() => {
		if (editing || seeded || defaults.isPending) return;
		seeded = true;
		const check = defaultSchedule('update_check', defaults.data);
		const run = defaultSchedule('update_run', defaults.data);
		checkCron = check.cron;
		checkZone = check.timeZone;
		runCron = run.cron;
		runZone = run.timeZone;
	});

	$effect(() => {
		if (editing || scope !== 'environment' || environmentId) return;
		const active = (envs.data ?? []).filter((e) => e.status !== 'archived');
		if (active.length === 1) environmentId = active[0].id;
	});

	// Standalone containers Docker Manager can recreate, across the environments
	// in scope (online ones only: offline environments can't be listed).
	$effect(() => {
		const selected =
			scope === 'all'
				? (envs.data ?? []).filter((e) => e.status !== 'archived' && e.online)
				: (envs.data ?? []).filter((e) => e.id === environmentId && e.online);
		const version = ++loadVersion;
		containers = [];
		containerError = false;
		containersLoading = selected.length > 0;
		if (!selected.length) return;
		void Promise.all(
			selected.map(async (env) => {
				const items = await fetchAllPages((cursor) =>
					unwrap(
						api.GET('/api/v1/environments/{environmentId}/containers', {
							params: {
								path: { environmentId: env.id },
								query: { cursor, limit: 200 }
							}
						})
					)
				);
				return items
					.filter((c) => c.managed?.kind === 'standalone' && !c.stack)
					.map((c) => ({
						key: scope === 'all' ? `${env.id}/${c.name}` : c.name,
						name: c.name,
						environment: env.name
					}));
			})
		)
			.then((groups) => {
				if (version === loadVersion) containers = groups.flat();
			})
			.catch(() => {
				if (version === loadVersion) containerError = true;
			})
			.finally(() => {
				if (version === loadVersion) containersLoading = false;
			});
	});

	useUnsaved(
		() => `Update policy ${name || 'draft'}`,
		() => touched && !busy
	);

	const envOptions = $derived(
		(envs.data ?? [])
			.filter((e) => e.status !== 'archived')
			.map((e) => ({ value: e.id, label: e.online ? e.name : `${e.name} (offline)` }))
	);
	const visibleStacks = $derived(
		(stacks.data ?? []).filter((s) => scope === 'all' || s.environmentId === environmentId)
	);
	const fields = $derived(fieldErrors(error));
	const windowError = $derived(
		windowOn && (!isClock(windowStart) || !isClock(windowEnd))
			? 'Use 24-hour times such as 01:00 and 05:30.'
			: null
	);
	const waitError = $derived.by(() => {
		const t = waitTimeout.trim();
		if (!t) return null;
		const n = Number(t);
		return Number.isInteger(n) && n >= 0 && n <= 3600
			? null
			: 'Enter whole seconds from 0 to 3600.';
	});
	const canSave = $derived(
		!!name.trim() &&
			(scope === 'all' || !!environmentId) &&
			!!checkCron.trim() &&
			!!runCron.trim() &&
			!windowError &&
			!waitError
	);

	function toggle(list: string[], value: string, exclude: boolean): string[] {
		touched = true;
		return exclude ? [...new Set([...list, value])] : list.filter((x) => x !== value);
	}

	function toggleDay(d: number, on: boolean) {
		touched = true;
		windowDays = on ? [...windowDays, d].sort() : windowDays.filter((x) => x !== d);
	}

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		if (!canSave) return;
		busy = true;
		error = null;
		const body = {
			scope,
			environmentId: scope === 'all' ? undefined : environmentId,
			name: name.trim(),
			excludeStacks: excludedStacks,
			excludeContainers: excludedContainers,
			checkSchedule: { cron: checkCron, timeZone: checkZone, enabled: checkEnabled },
			runSchedule: { cron: runCron, timeZone: runZone, enabled: runEnabled },
			window: windowOn ? { start: windowStart, end: windowEnd, days: windowDays } : undefined,
			waitTimeoutSeconds: waitTimeout.trim() ? Number(waitTimeout) : 0
		};
		try {
			const saved = p
				? await unwrap(
						api.PATCH('/api/v1/environment-update-policies/{policyId}', {
							params: {
								path: { policyId: p.id },
								header: { 'If-Match': ifMatch(p.revision) }
							},
							body
						})
					)
				: await unwrap(api.POST('/api/v1/environment-update-policies', { body }));
			touched = false;
			await qc.invalidateQueries({ queryKey: ['policies'] });
			qc.setQueryData(environmentUpdateKeys.detail(saved.id), saved);
			if (p) toast.success(`Saved update policy ${saved.name}`);
			else
				toast.success(`Created update policy ${saved.name}`, {
					body:
						checkEnabled || runEnabled
							? undefined
							: 'Its schedules are off: nothing runs until you turn them on.'
				});
			await goto(routes.updatePolicy(saved.id));
		} catch (e) {
			error = e;
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
				{actionError(error)}
			</Notice>
		{/if}

		<Card title="Scope">
			<Fields>
				<TextField
					label="Name"
					bind:value={name}
					required
					maxlength={100}
					error={fields['body.name']}
					placeholder="Image updates"
				/>
				{#if editing && p}
					<p class="muted">
						Covers {#if p.scope === 'all'}<strong class="strong"
								>all environments</strong
							>{:else}the environment <strong class="strong"
								>{environmentName(envs.data, p.environmentId)}</strong
							>{/if}. The scope can't change; create another policy for a different
						one.
					</p>
				{:else}
					{#if allowAll}
						<RadioGroup
							label="Environments"
							bind:value={scope}
							onchange={() => (touched = true)}
							options={[
								{
									value: 'all',
									label: 'All environments',
									description: 'Environments added later are covered too.'
								},
								{
									value: 'environment',
									label: 'One environment',
									description:
										'Only the stacks and containers of one environment.'
								}
							]}
						/>
					{/if}
					{#if scope === 'environment'}
						<Select
							label="Environment"
							options={envOptions}
							bind:value={environmentId}
							placeholder="Choose an environment"
							required
							error={fields['body.environmentId']}
							onchange={() => (touched = true)}
						/>
					{/if}
				{/if}
			</Fields>
		</Card>

		<Card
			title="Exclusions"
			subtitle="Every managed stack and Docker Manager-managed standalone container in scope is covered. Check the ones to leave out."
		>
			<Fields>
				<FieldGroup legend="Exclude stacks">
					{#if stacks.isPending}
						<Skeleton lines={3} height="20px" />
					{:else if !visibleStacks.length}
						<p class="muted">No managed stacks in scope.</p>
					{:else}
						<ChoiceGrid min="220px">
							{#each visibleStacks as stack (stack.id)}
								<Checkbox
									label={stack.displayName || stack.name}
									description={scope === 'all'
										? environmentName(envs.data, stack.environmentId)
										: undefined}
									checked={excludedStacks.includes(stack.id)}
									onchange={(e) =>
										(excludedStacks = toggle(
											excludedStacks,
											stack.id,
											e.currentTarget.checked
										))}
								/>
							{/each}
						</ChoiceGrid>
					{/if}
				</FieldGroup>
				<FieldGroup
					legend="Exclude standalone containers"
					hint="Only containers Docker Manager created and can recreate from their saved specification."
				>
					{#if containersLoading}
						<Skeleton lines={2} height="20px" />
					{:else if containerError}
						<p class="muted">
							The containers can't be listed right now. Saved exclusions are kept.
						</p>
					{:else if !containers.length}
						<p class="muted">
							No Docker Manager-managed standalone containers in scope.
						</p>
					{:else}
						<ChoiceGrid min="220px">
							{#each containers as container (container.key)}
								<Checkbox
									label={container.name}
									description={scope === 'all'
										? container.environment
										: undefined}
									checked={excludedContainers.includes(container.key)}
									onchange={(e) =>
										(excludedContainers = toggle(
											excludedContainers,
											container.key,
											e.currentTarget.checked
										))}
								/>
							{/each}
						</ChoiceGrid>
					{/if}
				</FieldGroup>
				<p class="muted">
					A container labelled <code>docker-manager.update.exclude=true</code> is always left
					out.
				</p>
			</Fields>
		</Card>

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
								placeholder="02:00"
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
					error={waitError ?? fields['body.waitTimeoutSeconds']}
				/>
			</Fields>
		</Card>
	</Page>

	<FormFooter>
		<Button href={p ? routes.updatePolicy(p.id) : routes.updates()} variant="ghost"
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
