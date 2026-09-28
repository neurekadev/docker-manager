<script lang="ts">
	// One backup set in the details drawer (#10): when and how it ran, its
	// size, the live progress while it runs, and every backup it holds
	// grouped by environment with its own state and time, each linked to
	// its backup.
	import { createQuery } from '@tanstack/svelte-query';
	import { routes } from '$lib/routes';
	import { Badge, formatBytes, formatDateTime, formatDuration } from '$lib/ui';
	import Facts from '$lib/features/common/Facts.svelte';
	import RunningBackups from './RunningBackups.svelte';
	import SetMembers from './SetMembers.svelte';
	import {
		membersByEnvironment,
		setDuration,
		setState,
		setSummary,
		type BackupActivity,
		type BackupSet
	} from './model';
	import { backupsQuery } from './queries';

	interface Props {
		set: BackupSet;
		policyId?: string;
		policyName?: string;
		environmentName: (id: string) => string;
		bytes?: number;
		/** Running jobs of this set. */
		activity?: BackupActivity[];
	}

	let { set: s, policyId, policyName, environmentName, bytes, activity = [] }: Props = $props();
	// The set's backups, so its members link to them.
	const backups = createQuery(() => ({ ...backupsQuery({ setId: s.id }), retry: false }));
	const st = $derived(setState(s.state));
	const duration = $derived(setDuration(s));
	const origin = $derived(
		s.origin === 'scheduled'
			? 'Scheduled'
			: s.origin === 'api_token'
				? 'API token'
				: 'Started by hand'
	);
</script>

<div class="detail">
	<Facts
		columns={2}
		items={[
			{ label: 'State', render: stateFact },
			{ label: 'Backups', value: setSummary(s) },
			{ label: 'Started', value: formatDateTime(s.startedAt) },
			{
				label: 'Duration',
				value: duration !== undefined ? formatDuration(duration) : 'Still running'
			},
			{ label: 'Origin', value: origin },
			{ label: 'Size', value: bytes !== undefined ? formatBytes(bytes) : '—' },
			...(policyId ? [{ label: 'Policy', render: policyFact }] : [])
		]}
	/>
	{#snippet stateFact()}<Badge tone={st.tone} dot>{st.label}</Badge>{/snippet}
	{#snippet policyFact()}<a href={routes.backupPolicy(policyId ?? '')}
			>{policyName ?? 'Open policy'}</a
		>{/snippet}

	{#if activity.length}
		<section aria-labelledby="set-{s.id}-running">
			<h3 id="set-{s.id}-running">Running now</h3>
			<RunningBackups
				jobs={activity}
				policyName={() => policyName ?? 'Backup'}
				{environmentName}
				compact
			/>
		</section>
	{/if}

	{#each membersByEnvironment(s) as [env, members] (env)}
		<section aria-labelledby="set-{s.id}-env-{env || 'manager'}">
			<h3 id="set-{s.id}-env-{env || 'manager'}">
				{env ? environmentName(env) : 'Manager'}
				<span class="muted num">{members.length}</span>
			</h3>
			<SetMembers {members} backups={backups.data} />
		</section>
	{/each}
</div>

<style>
	.detail {
		display: flex;
		flex-direction: column;
		gap: var(--space-5);
	}

	section {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}

	h3 {
		display: flex;
		align-items: baseline;
		gap: var(--space-2);
		margin: 0;
		color: var(--text-strong);
		font-size: var(--text-body);
		font-weight: var(--weight-semibold);
	}
</style>
