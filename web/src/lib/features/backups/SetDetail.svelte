<script lang="ts">
	// One backup set in the details drawer (#10): when and how it ran, its
	// size, the live progress while it runs, and every backup it holds
	// grouped by environment with its own state, time and repository (a
	// copy per repository, #246), each linked to its backup.
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

	interface Props {
		set: BackupSet;
		environmentName: (id: string) => string;
		repositoryName?: (id: string) => string;
		bytes?: number;
		/** Running jobs of this set. */
		activity?: BackupActivity[];
	}

	let { set: s, environmentName, repositoryName, bytes, activity = [] }: Props = $props();
	const st = $derived(setState(s.state));
	const duration = $derived(setDuration(s));
	const origin = $derived(
		s.origin === 'scheduled'
			? 'Scheduled'
			: s.origin === 'api_token'
				? 'API Token'
				: 'Started by Hand'
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
			{ label: 'Size', value: bytes !== undefined ? formatBytes(bytes) : '—' }
		]}
	/>
	{#snippet stateFact()}<Badge tone={st.tone} dot>{st.label}</Badge>{/snippet}

	{#if activity.length}
		<section aria-labelledby="set-{s.id}-running">
			<h3 id="set-{s.id}-running">Running Now</h3>
			<RunningBackups
				jobs={activity}
				policyName={() => 'Backups'}
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
			<SetMembers {members} {repositoryName} />
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
