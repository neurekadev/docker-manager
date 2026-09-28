<script lang="ts">
	// One schedule (#13): its next runs in the policy's time zone with DST
	// annotations (POST /schedules/previews, the one cron parser) and its
	// recent runs, one line each: what the run's jobs did in one sentence
	// ("20 checks, all succeeded", "2 of 20 checks failed") linking to its
	// failed job or the jobs of that kind, and why a run did not start
	// (missed, skipped, refused). Five runs at first, more on request.
	import { createQuery } from '@tanstack/svelte-query';
	import type { Schedule } from '$lib/api/client';
	import { schedulePreviewQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		ErrorState,
		Notice,
		Skeleton,
		StatusBadge,
		describeCron,
		formatDateTime
	} from '$lib/ui';
	import { runSummary } from '$lib/features/jobs/runs';
	import { dstLabel, formatRunTime, policyHref, runReason, runStatus } from './model';

	let { schedule }: { schedule: Schedule } = $props();

	const preview = createQuery(() =>
		schedulePreviewQuery(schedule.cron, schedule.timeZone, schedule.kind)
	);
	const PAGE = 5;
	let shown = $state(PAGE);
	const runs = $derived(schedule.recentRuns.slice(0, shown));

	function runLink(r: Schedule['recentRuns'][number]): { href: string; label: string } | null {
		if (!r.jobs.length) return null;
		const failed = r.jobs.find((j) => ['failed', 'partial', 'interrupted'].includes(j.state));
		const one = failed ?? (r.jobs.length === 1 ? r.jobs[0] : undefined);
		return one
			? { href: routes.job(one.jobId), label: failed ? 'Open the failed job' : 'Open job' }
			: { href: routes.jobs(r.jobs[0].kind), label: 'Open jobs' };
	}
</script>

<div class="detail">
	<dl class="facts">
		<dt>Kind</dt>
		<dd>{schedule.kindLabel}</dd>
		<dt>Schedule</dt>
		<dd title="{schedule.cron} ({schedule.timeZone})">
			{describeCron(schedule.cron, schedule.timeZone)}
		</dd>
		<dt>Time zone</dt>
		<dd>{schedule.timeZone}</dd>
		<dt>After downtime</dt>
		<dd>
			{schedule.catchUp === 'once' ? 'One catch-up run' : 'Missed runs are recorded, not run'}
		</dd>
	</dl>

	{#if schedule.invalidReason}
		<Notice tone="danger" title="This schedule cannot run" live="none">
			{schedule.invalidReason} Fix the schedule in the policy.
		</Notice>
	{:else if !schedule.enabled}
		<Notice tone="info" title="Disabled" live="none">
			Nothing runs on this schedule until the policy is enabled. The next times below are a
			preview.
		</Notice>
	{/if}

	<section aria-labelledby="next-{schedule.id}">
		<h3 id="next-{schedule.id}" class="subsection-title">Next runs</h3>
		{#if preview.isPending}
			<Skeleton lines={5} height="16px" />
		{:else if preview.isError}
			<ErrorState
				error={preview.error}
				title="The next runs could not be calculated."
				bare
				compact
			/>
		{:else}
			<ol class="runs">
				{#each preview.data?.runs ?? [] as r (r.utc)}
					<li>
						<span class="num" title={formatDateTime(r.utc)}
							>{formatRunTime(r.at, schedule.timeZone)}</span
						>
						{#if dstLabel(r)}
							<Badge tone="warn" title={r.dstNote}>{dstLabel(r)}</Badge>
						{/if}
						{#if r.dstNote}<p class="note">{r.dstNote}.</p>{/if}
					</li>
				{/each}
			</ol>
		{/if}
	</section>

	<section aria-labelledby="history-{schedule.id}">
		<h3 id="history-{schedule.id}" class="subsection-title">Recent runs</h3>
		{#if runs.length}
			<ol class="runs">
				{#each runs as r (r.scheduledFor)}
					{@const s = runStatus(r)}
					{@const link = runLink(r)}
					<li>
						<span class="num" title={formatDateTime(r.scheduledFor)}
							>{formatRunTime(r.scheduledFor, schedule.timeZone)}</span
						>
						<StatusBadge status={s.status} kind={s.kind} label={s.label || undefined} />
						{#if r.catchUp}<Badge tone="info">Catch-up</Badge>{/if}
						{#if r.jobs.length}
							<p class="note">
								{runSummary(r.jobs, r.jobs[0].kind).text}{#if link}
									· <a href={link.href}>{link.label}</a>{/if}
							</p>
						{:else if runReason(r)}
							<p class="note">{runReason(r)}</p>
						{/if}
					</li>
				{/each}
			</ol>
			{#if schedule.recentRuns.length > shown}
				<div class="more">
					<Button size="sm" variant="ghost" onclick={() => (shown += PAGE)}
						>Load more</Button
					>
				</div>
			{/if}
		{:else}
			<p class="muted">No runs yet. They appear here, including missed and skipped ones.</p>
		{/if}
	</section>

	<Button href={policyHref(schedule.kind, schedule.policyId)}>Edit the policy</Button>
</div>

<style>
	.detail {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: var(--space-5);
		padding: var(--space-4) var(--space-5) var(--space-6);
	}

	.detail > :global(*) {
		max-width: 100%;
	}

	section,
	.facts {
		align-self: stretch;
	}

	.facts {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: var(--space-2) var(--space-4);
		margin: 0;
	}

	dt {
		color: var(--text-muted);
	}

	dd {
		margin: 0;
	}

	h3 {
		margin-bottom: var(--space-2);
	}

	.runs {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.runs li {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
		padding-bottom: var(--space-2);
		border-bottom: 1px solid var(--border-subtle);
	}

	.note {
		flex-basis: 100%;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.more {
		margin-top: var(--space-2);
	}
</style>
