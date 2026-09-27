<script lang="ts">
	// One backup policy in the details drawer (#10): what it covers and
	// where it goes, its schedule and retention, and its recent sets.
	import { Badge, formatDateTime } from '$lib/ui';
	import Facts from '$lib/features/common/Facts.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import {
		retentionText,
		scopeText,
		setState,
		setSummary,
		type BackupPolicy,
		type BackupRepository
	} from './model';

	interface Props {
		policy: BackupPolicy;
		repositories: BackupRepository[] | undefined;
		environmentName: (id: string) => string;
	}

	let { policy: p, repositories, environmentName }: Props = $props();
	const repoName = (id: string | undefined) =>
		repositories?.find((r) => r.id === id)?.name ?? '—';
	const explicit = $derived((p.stacks ?? []).length > 0 || (p.volumes ?? []).length > 0);
	const leftOut = $derived(
		[
			[(p.excludeStacks ?? []).length, 'stack', 'stacks'],
			[(p.excludeVolumes ?? []).length, 'volume', 'volumes']
		]
			.filter(([n]) => (n as number) > 0)
			.map(([n, one, many]) => `${n} ${n === 1 ? one : many}`)
			.join(', ') || 'Nothing'
	);
</script>

<div class="detail">
	<section aria-labelledby="pol-{p.id}-what">
		<h3 id="pol-{p.id}-what">What is backed up</h3>
		<Facts
			columns={1}
			items={[
				{ label: 'Covers', value: scopeText(p, environmentName) },
				explicit
					? {
							label: 'Selection',
							value: `${(p.stacks ?? []).length} stacks, ${(p.volumes ?? []).length} standalone volumes`
						}
					: { label: 'Left out', value: leftOut },
				{
					label: 'Anonymous volumes',
					value: p.anonymousVolumes ? 'Backed up' : 'Not backed up'
				},
				{
					label: 'Manager state',
					value: p.includeManagerState
						? p.includeMetrics
							? 'Yes, with metrics'
							: 'Yes, without metrics'
						: 'No'
				},
				{
					label: 'Containers during backups',
					value: p.shutdown ? 'Stopped, then started again' : 'Keep running (live)'
				}
			]}
		/>
	</section>

	<section aria-labelledby="pol-{p.id}-when">
		<h3 id="pol-{p.id}-when">Schedule, retention and destination</h3>
		<Facts
			columns={1}
			items={[
				{ label: 'Schedule', render: sched },
				{ label: 'Retention', value: retentionText(p.retention) },
				{ label: 'Repository', value: repoName(p.repositoryId) },
				...Object.entries(p.environmentRepositories ?? {}).map(([env, r]) => ({
					label: `Repository for ${environmentName(env)}`,
					value: repoName(r)
				}))
			]}
		/>
		{#snippet sched()}
			{#if p.schedule}<ScheduleSummary
					{...p.schedule}
					nextRun={p.schedule.nextRun}
				/>{:else}<span class="muted">Runs only when you start it</span>{/if}
		{/snippet}
	</section>

	<section aria-labelledby="pol-{p.id}-sets">
		<h3 id="pol-{p.id}-sets">Recent sets</h3>
		{#if p.recentSets?.length}
			<ul class="sets" role="list">
				{#each p.recentSets.slice(0, 5) as s (s.id)}
					{@const st = setState(s.state)}
					<li>
						<Badge tone={st.tone} dot>{st.label}</Badge>
						<span class="num">{formatDateTime(s.startedAt)}</span>
						<span class="muted">{setSummary(s)}</span>
					</li>
				{/each}
			</ul>
		{:else}
			<p class="muted">This policy has not run yet.</p>
		{/if}
	</section>
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
		margin: 0;
		color: var(--text-strong);
		font-size: var(--text-body);
		font-weight: var(--weight-semibold);
	}

	.sets {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.sets li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-3);
		font-size: var(--text-caption);
	}

	p {
		margin: 0;
	}
</style>
