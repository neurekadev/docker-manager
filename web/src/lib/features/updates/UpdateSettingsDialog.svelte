<script lang="ts">
	// Edit the update settings (#20, #240) in a dialog: what they cover on
	// the left (environments, stacks and standalone containers, all
	// included until unchecked, also ones added later), when they run on
	// the right (Check Automatically and Update Automatically, #13, both off
	// until turned on, their schedule fields shown only while on; an
	// optional update window and the health wait). Saving never runs
	// anything. Render it only while open ({#if}): each opening starts
	// from `settings`.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, unwrap } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import {
		Button,
		Checkbox,
		CronField,
		Dialog,
		Notice,
		Skeleton,
		Switch,
		TextField,
		toast
	} from '$lib/ui';
	import ChoiceGrid from '$lib/features/common/ChoiceGrid.svelte';
	import CoverageList from '$lib/features/common/CoverageList.svelte';
	import {
		environmentName,
		fetchAllPages,
		ifMatch,
		stacksQuery
	} from '$lib/features/common/data';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import { DAY_OPTIONS, isClock } from './model';
	import { updateSettingsKeys, type UpdateSettings } from './queries';

	let {
		open = $bindable(true),
		settings
	}: {
		open?: boolean;
		settings: UpdateSettings;
	} = $props();

	const qc = useQueryClient();
	// Form state is initialised once from the settings: a live refetch
	// never overwrites what the user is typing.
	const p = untrack(() => settings);
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());

	let excludedEnvironments = $state<string[]>([...p.excludeEnvironments]);
	let excludedStacks = $state<string[]>([...p.excludeStacks]);
	let excludedContainers = $state<string[]>([...p.excludeContainers]);
	let checkCron = $state(p.checkSchedule.cron ?? '');
	let checkZone = $state(p.checkSchedule.timeZone ?? '');
	let checkEnabled = $state(p.checkSchedule.enabled);
	let runCron = $state(p.runSchedule.cron ?? '');
	let runZone = $state(p.runSchedule.timeZone ?? '');
	let runEnabled = $state(p.runSchedule.enabled);
	let windowOn = $state(!!p.window);
	let windowStart = $state(p.window?.start ?? '02:00');
	let windowEnd = $state(p.window?.end ?? '05:00');
	let windowDays = $state<number[]>(p.window?.days ?? []);
	let waitTimeout = $state(p.waitTimeoutSeconds ? String(p.waitTimeoutSeconds) : '');
	let busy = $state(false);
	let touched = $state(false);
	let error = $state<unknown>(null);
	let containers = $state<{ key: string; name: string; environment: string }[]>([]);
	let containersLoading = $state(false);
	let containerError = $state(false);
	let loadVersion = 0;

	const activeEnvironments = $derived((envs.data ?? []).filter((e) => e.status !== 'archived'));
	const covered = (environmentId: string) => !excludedEnvironments.includes(environmentId);

	// Standalone containers Docker Manager can recreate, across the covered
	// environments (online ones only: offline environments can't be listed).
	$effect(() => {
		const selected = activeEnvironments.filter((e) => e.online && covered(e.id));
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
						key: `${env.id}/${c.name}`,
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
		() => 'Update settings',
		() => touched && !busy
	);

	const visibleStacks = $derived((stacks.data ?? []).filter((s) => covered(s.environmentId)));
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
	const canSave = $derived(!!checkCron.trim() && !!runCron.trim() && !windowError && !waitError);

	function toggleDay(d: number, on: boolean) {
		touched = true;
		windowDays = on ? [...windowDays, d].sort() : windowDays.filter((x) => x !== d);
	}

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		if (!canSave) return;
		busy = true;
		error = null;
		try {
			const saved = await unwrap(
				api.PATCH('/api/v1/update-settings', {
					params: { header: { 'If-Match': ifMatch(p.revision) } },
					body: {
						excludeEnvironments: excludedEnvironments,
						excludeStacks: excludedStacks,
						excludeContainers: excludedContainers,
						checkSchedule: {
							cron: checkCron,
							timeZone: checkZone,
							enabled: checkEnabled
						},
						runSchedule: { cron: runCron, timeZone: runZone, enabled: runEnabled },
						...(windowOn
							? { window: { start: windowStart, end: windowEnd, days: windowDays } }
							: { clearWindow: true }),
						waitTimeoutSeconds: waitTimeout.trim() ? Number(waitTimeout) : 0
					}
				})
			);
			touched = false;
			qc.setQueryData(updateSettingsKeys.settings, saved);
			await qc.invalidateQueries({ queryKey: ['policies'] });
			toast.success('Saved the update settings', {
				body:
					saved.checkSchedule.enabled || saved.runSchedule.enabled
						? undefined
						: 'Both schedules are off: nothing runs until you turn them on.'
			});
			open = false;
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title="Edit Updates"
	description="Your Compose files and tags never change."
	size="xl"
	dismissible={!busy}
>
	<form id="update-settings-form" onsubmit={submit} oninput={() => (touched = true)} novalidate>
		{#if error && Object.keys(fields).length === 0}
			<div class="error">
				<Notice tone="danger" title="The settings were not saved" live="alert">
					{actionError(error)}
				</Notice>
			</div>
		{/if}
		<div class="layout">
			<section class="col" aria-labelledby="upd-covers">
				<h3 id="upd-covers" class="section">What It Covers</h3>
				<Fields>
					<FieldGroup
						legend="Environments"
						hint="Uncheck an environment to leave it out. Environments added later are covered."
					>
						{#if envs.isPending}
							<Skeleton lines={2} height="20px" />
						{:else}
							<CoverageList
								label="Environments Covered"
								min="180px"
								items={activeEnvironments.map((e) => ({
									key: e.id,
									label: e.name,
									description: e.online ? undefined : 'Offline'
								}))}
								excluded={excludedEnvironments}
								onchange={(v) => {
									excludedEnvironments = v;
									touched = true;
								}}
							/>
						{/if}
					</FieldGroup>
					<FieldGroup legend="Stacks" hint="Uncheck a stack to leave it out.">
						{#if stacks.isPending}
							<Skeleton lines={3} height="20px" />
						{:else if !visibleStacks.length}
							<p class="muted">No managed stacks in the covered environments.</p>
						{:else}
							<CoverageList
								label="Stacks Covered"
								min="200px"
								items={visibleStacks.map((s) => ({
									key: s.id,
									label: s.displayName || s.name,
									description: environmentName(envs.data, s.environmentId)
								}))}
								excluded={excludedStacks}
								onchange={(v) => {
									excludedStacks = v;
									touched = true;
								}}
							/>
						{/if}
					</FieldGroup>
					<FieldGroup
						legend="Standalone Containers"
						hint="Containers Docker Manager created. Uncheck one to leave it out."
						info="A container labelled docker-manager.update.exclude=true is always left out."
					>
						{#if containersLoading}
							<Skeleton lines={2} height="20px" />
						{:else if containerError}
							<p class="muted">
								The containers can't be listed right now. Saved exclusions are kept.
							</p>
						{:else if !containers.length}
							<p class="muted">
								No Docker Manager-managed standalone containers in the covered
								environments.
							</p>
						{:else}
							<CoverageList
								label="Standalone Containers Covered"
								min="200px"
								items={containers.map((c) => ({
									key: c.key,
									label: c.name,
									description: c.environment
								}))}
								excluded={excludedContainers}
								onchange={(v) => {
									excludedContainers = v;
									touched = true;
								}}
							/>
						{/if}
					</FieldGroup>
				</Fields>
			</section>

			<section class="col" aria-labelledby="upd-when">
				<h3 id="upd-when" class="section">When It Runs</h3>
				<Fields>
					<FieldGroup legend="Checks" hint="Checks never pull or change anything.">
						<Switch
							label="Check Automatically"
							bind:checked={checkEnabled}
							onchange={() => (touched = true)}
						/>
						{#if checkZone && checkEnabled}
							<CronField
								label="Check Schedule"
								kind="update_check"
								bind:cron={checkCron}
								bind:timeZone={checkZone}
							/>
						{/if}
					</FieldGroup>
					<FieldGroup legend="Updates">
						<Switch
							label="Update Automatically"
							bind:checked={runEnabled}
							onchange={() => (touched = true)}
						/>
						{#if runEnabled}
							<Notice
								tone="warn"
								icon={TriangleAlert}
								title="Containers Restart Without Asking"
								live="none"
							>
								At each time of the update schedule, Docker Manager applies what the
								last check found and recreates those containers. A failed update is
								not rolled back: its digest is quarantined and you pin a working
								image yourself.
							</Notice>
						{/if}
						{#if runZone && runEnabled}
							<CronField
								label="Update Schedule"
								kind="update_run"
								bind:cron={runCron}
								bind:timeZone={runZone}
							/>
						{/if}
					</FieldGroup>
					<Disclosure
						summary="Update Window and Health Wait"
						open={windowOn || !!waitTimeout}
					>
						<Switch
							label="Only Update Inside a Window"
							description="Applies to scheduled updates only."
							bind:checked={windowOn}
							onchange={() => (touched = true)}
						/>
						{#if windowOn}
							<FieldGroup legend="Update Window" hint="No day checked: every day.">
								<ChoiceGrid min="64px">
									{#each DAY_OPTIONS as d (d.value)}
										<Checkbox
											label={d.label}
											checked={windowDays.includes(d.value)}
											onchange={(e) =>
												toggleDay(d.value, e.currentTarget.checked)}
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
										info="Before the start time: the window spans midnight."
									/>
								</Fields>
							</FieldGroup>
						{/if}
						<TextField
							label="Health Wait"
							optional
							description="Seconds to wait for recreated containers to become healthy."
							bind:value={waitTimeout}
							inputmode="numeric"
							error={waitError ?? fields['body.waitTimeoutSeconds']}
						/>
					</Disclosure>
				</Fields>
			</section>
		</div>
	</form>
	{#snippet footer()}
		<Button variant="ghost" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
		<Button
			type="submit"
			form="update-settings-form"
			variant="primary"
			loading={busy}
			disabled={!canSave}
		>
			Save Changes
		</Button>
	{/snippet}
</Dialog>

<style>
	.layout {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-6);
		align-items: start;
	}

	.col {
		display: grid;
		gap: var(--space-3);
		min-width: 0;
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
		.layout {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
