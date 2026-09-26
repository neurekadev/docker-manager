<script lang="ts">
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { api, unwrap } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { Button, Card, Notice, toast } from '$lib/ui';
	import { fetchAllPages, ifMatch, stacksQuery } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import { defaultSchedule, scheduleDefaultsQuery } from '$lib/features/common/schedules';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
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
	const p = untrack(() => initial);
	const initialEnvironment = untrack(() => suggestedEnvironment);
	const initialAllowAll = untrack(() => allowAll);
	const qc = useQueryClient();
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
	let windowEnabled = $state(!!p?.window);
	let windowStart = $state(p?.window?.start ?? '02:00');
	let windowEnd = $state(p?.window?.end ?? '05:00');
	let windowDays = $state<number[]>(p?.window?.days ?? []);
	let waitSeconds = $state(p?.waitTimeoutSeconds ?? 0);
	let busy = $state(false);
	let touched = $state(false);
	let error = $state<unknown>(null);
	let containers = $state<{ key: string; name: string }[]>([]);
	let containerError = $state(false);
	let loadVersion = 0;

	$effect(() => {
		if (p || defaults.isPending || checkCron) return;
		const check = defaultSchedule('update_check', defaults.data);
		const run = defaultSchedule('update_run', defaults.data);
		checkCron = check.cron;
		checkZone = check.timeZone;
		runCron = run.cron;
		runZone = run.timeZone;
	});

	$effect(() => {
		const selected =
			scope === 'all'
				? (envs.data ?? []).filter((e) => e.status !== 'archived' && e.online)
				: (envs.data ?? []).filter((e) => e.id === environmentId && e.online);
		const version = ++loadVersion;
		containers = [];
		containerError = false;
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
						name: `${env.name} / ${c.name}`
					}));
			})
		)
			.then((groups) => {
				if (version === loadVersion) containers = groups.flat();
			})
			.catch(() => {
				if (version === loadVersion) containerError = true;
			});
	});

	useUnsaved(
		() => `Update policy ${name || 'draft'}`,
		() => touched && !busy
	);
	const visibleStacks = $derived(
		(stacks.data ?? []).filter((s) => scope === 'all' || s.environmentId === environmentId)
	);
	function toggle(list: string[], value: string, exclude: boolean): string[] {
		touched = true;
		return exclude ? [...new Set([...list, value])] : list.filter((x) => x !== value);
	}

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		if (scope === 'environment' && !environmentId) return;
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
			window: windowEnabled
				? { start: windowStart, end: windowEnd, days: windowDays }
				: undefined,
			waitTimeoutSeconds: Number(waitSeconds)
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
			toast.success(p ? 'Update policy saved' : 'Update policy created');
			await goto(routes.updatePolicy(saved.id));
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
</script>

<form onsubmit={submit} oninput={() => (touched = true)}>
	{#if error}<Notice tone="danger" title="The policy could not be saved"
			>{actionError(error)}</Notice
		>{/if}
	<Card title="Scope">
		<div class="policy-fields">
			<label
				>Name <input
					required
					maxlength="100"
					bind:value={name}
					placeholder="Image updates"
				/></label
			>
			<label
				>Environments
				<select bind:value={scope} disabled={!!p}>
					{#if allowAll || p?.scope === 'all'}<option value="all">All Environments</option
						>{/if}<option value="environment">Single Environment</option>
				</select>
			</label>
			{#if scope === 'environment'}
				<label
					>Environment
					<select bind:value={environmentId} required disabled={!!p}>
						<option value="">Choose an environment</option>
						{#each (envs.data ?? []).filter((e) => e.status !== 'archived') as env (env.id)}
							<option value={env.id}>{env.name}</option>
						{/each}
					</select>
				</label>
			{/if}
		</div>
	</Card>
	<Card
		title="Exclusions"
		subtitle="All managed stacks and standalone containers in the selected environments are covered by default."
	>
		<h3>Stacks</h3>
		{#each visibleStacks as stack (stack.id)}
			<label class="policy-check"
				><input
					type="checkbox"
					checked={excludedStacks.includes(stack.id)}
					onchange={(e) =>
						(excludedStacks = toggle(
							excludedStacks,
							stack.id,
							e.currentTarget.checked
						))}
				/>
				Exclude {stack.displayName || stack.name}</label
			>
		{/each}
		{#if !visibleStacks.length}<p>No managed stacks are visible.</p>{/if}
		<h3>Standalone containers</h3>
		{#each containers as container (container.key)}
			<label class="policy-check"
				><input
					type="checkbox"
					checked={excludedContainers.includes(container.key)}
					onchange={(e) =>
						(excludedContainers = toggle(
							excludedContainers,
							container.key,
							e.currentTarget.checked
						))}
				/>
				Exclude {container.name}</label
			>
		{/each}
		{#if containerError}<p>
				Containers could not be loaded. Saved exclusions are retained.
			</p>{/if}
		<p>
			Set <code>dockyard.update.exclude=true</code> on a container to keep it out of automatic updates.
		</p>
	</Card>
	<Card title="Schedule">
		<div class="policy-fields">
			<label
				><input type="checkbox" bind:checked={checkEnabled} /> Check for updates automatically</label
			>
			<label>Check cron <input required bind:value={checkCron} /></label>
			<label>Check time zone <input required bind:value={checkZone} /></label>
			<label
				><input type="checkbox" bind:checked={runEnabled} /> Apply updates automatically</label
			>
			<label>Run cron <input required bind:value={runCron} /></label>
			<label>Run time zone <input required bind:value={runZone} /></label>
			<label
				><input type="checkbox" bind:checked={windowEnabled} /> Limit scheduled runs to a time
				window</label
			>
			{#if windowEnabled}
				<label>Window start <input type="time" required bind:value={windowStart} /></label>
				<label>Window end <input type="time" required bind:value={windowEnd} /></label>
				<fieldset>
					<legend>Days (none selected means every day)</legend>
					{#each ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'] as day, index (day)}
						<label
							><input
								type="checkbox"
								checked={windowDays.includes(index)}
								onchange={(e) =>
									(windowDays = e.currentTarget.checked
										? [...windowDays, index].sort()
										: windowDays.filter((d) => d !== index))}
							/>
							{day}</label
						>
					{/each}
				</fieldset>
			{/if}
			<label
				>Wait timeout (seconds) <input
					type="number"
					min="0"
					max="3600"
					bind:value={waitSeconds}
				/></label
			>
		</div>
	</Card>
	<Button
		variant="primary"
		type="submit"
		disabled={busy || !name.trim() || (scope === 'environment' && !environmentId)}
		>{busy ? 'Saving…' : 'Save policy'}</Button
	>
</form>

<style>
	.policy-fields {
		display: grid;
		gap: 1rem;
		max-width: 34rem;
	}
	.policy-fields label {
		display: grid;
		gap: 0.35rem;
	}
	.policy-check {
		display: block;
		margin: 0.5rem 0;
	}
	h3 {
		margin: 1rem 0 0.5rem;
	}
</style>
