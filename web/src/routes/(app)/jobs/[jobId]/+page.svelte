<script lang="ts">
	// Job detail (#26): live progress (JobProgress follows the job's event
	// stream), per-item results with partial failures, what blocks it,
	// attempts, targets and locks, the error class with recovery guidance,
	// and Cancel while the job can still stop at a safe point.
	import { page } from '$app/state';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Activity from '@lucide/svelte/icons/activity';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import Clock from '@lucide/svelte/icons/clock';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Server from '@lucide/svelte/icons/server';
	import CircleStop from '@lucide/svelte/icons/circle-stop';
	import { api, unwrap } from '$lib/api/client';
	import {
		environmentsQuery,
		jobQuery,
		myPermissionsQuery,
		stacksSummaryQuery
	} from '$lib/api/queries';
	import { accessOf, hasAny } from '$lib/shell/nav';
	import {
		ORIGIN_LABELS,
		blockedText,
		jobActive,
		jobDuration,
		jobKindLabel,
		jobTitle,
		policyPage,
		stackNames,
		targetText
	} from '$lib/features/jobs/labels';
	import { liveKeys } from '$lib/live/keys';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		ConfirmDialog,
		CopyButton,
		ErrorState,
		JobProgress,
		Notice,
		PageHeader,
		Skeleton,
		StatusBadge,
		formatDateTime,
		formatRelative,
		toast,
		type MetaItem
	} from '$lib/ui';

	const id = $derived(page.params.jobId ?? '');
	const qc = useQueryClient();
	const job = createQuery(() => jobQuery(id));
	const j = $derived(job.data);
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const stacks = createQuery(() => ({
		...stacksSummaryQuery(),
		enabled: hasAny(accessOf(perms.data), 'stack.')
	}));
	const nameOf = $derived(stackNames(stacks.data));
	const envName = (envId?: string) =>
		envId ? (envs.data?.find((e) => e.id === envId)?.name ?? envId.slice(0, 8)) : undefined;

	const title = $derived(j ? jobTitle(j, nameOf) : 'Job');
	usePage(() => ({
		title,
		crumbs: [{ label: 'Jobs', href: routes.jobs() }, { label: j ? jobKindLabel(j.kind) : '…' }]
	}));

	const meta = $derived.by<MetaItem[]>(() => {
		if (!j) return [];
		const out: MetaItem[] = [];
		const env = envName(j.environmentId);
		if (env) out.push({ icon: Server, label: env });
		out.push({
			icon: j.origin === 'scheduled' ? CalendarClock : Activity,
			label: ORIGIN_LABELS[j.origin] ?? j.origin
		});
		out.push({
			icon: Clock,
			label: `Started ${formatRelative(j.createdAt)}`,
			title: formatDateTime(j.createdAt)
		});
		if (j.attempt > 1) out.push({ icon: RotateCw, label: `Attempt ${j.attempt}` });
		return out;
	});

	let cancelOpen = $state(false);
	async function cancel() {
		const out = await unwrap(
			api.POST('/api/v1/jobs/{jobId}/cancellations', { params: { path: { jobId: id } } })
		);
		qc.setQueryData(liveKeys.item('jobs', id), out);
		await qc.invalidateQueries({ queryKey: liveKeys.list('jobs') });
		toast.info(`Cancelling ${jobKindLabel(out.kind).toLowerCase()}`, {
			body: 'It stops at the next safe point. Cleanup steps still run.'
		});
	}

	const timeline = $derived(
		j
			? [
					{ label: 'Queued', at: j.createdAt },
					{ label: 'Sent to its executor', at: j.dispatchedAt },
					{ label: 'Started', at: j.startedAt },
					{ label: 'Finished', at: j.finishedAt }
				].filter((t) => t.at)
			: []
	);
</script>

{#if job.isError}
	<ErrorState
		error={job.error}
		title="This job could not be loaded."
		onretry={() => job.refetch()}
	/>
{:else if !j}
	<div class="page" aria-busy="true">
		<Skeleton height="72px" radius="lg" />
		<Skeleton height="200px" radius="lg" />
	</div>
{:else}
	<div class="page">
		<PageHeader {title} icon={Activity} color="violet" {meta}>
			{#snippet status()}<StatusBadge status={j.state} kind="job" />{/snippet}
			{#snippet actions()}
				{#if j.cancellable && !j.cancelRequested && jobActive(j.state)}
					<Button
						variant="danger-soft"
						icon={CircleStop}
						onclick={() => (cancelOpen = true)}>Cancel job</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if j.blockedBy}
			<Notice tone="warn" title="Blocked" live="none">
				{blockedText(j.blockedBy)}
				{#if j.blockedBy.jobId}
					<a href={routes.job(j.blockedBy.jobId)}>Open the blocking job</a>.
				{/if}
			</Notice>
		{/if}
		{#if j.cancelRequested && jobActive(j.state)}
			<Notice tone="info" title="Cancellation requested" live="status">
				The job stops at its next safe point; cleanup steps (such as restarting stopped
				containers) still run.
			</Notice>
		{/if}

		{#key id}
			<JobProgress
				jobId={id}
				{title}
				variant="panel"
				notices={null}
				onfinish={() => job.refetch()}
			/>
		{/key}

		<div class="grid">
			<Card title="Details">
				<dl class="facts">
					<dt>Kind</dt>
					<dd class="mono">{j.kind}</dd>
					<dt>Runs on</dt>
					<dd>
						{j.executor === 'agent'
							? 'The environment’s agent'
							: 'The DockYard manager'}
					</dd>
					<dt>Duration</dt>
					<dd class="num">{jobDuration(j) || '—'}</dd>
					<dt>Attempt</dt>
					<dd class="num">
						{j.attempt}
						{#if j.attempt > 1}<span class="muted"
								>(resumed after an interruption or a lost connection)</span
							>{/if}
					</dd>
					{#if j.policyId}
						{@const pol = policyPage(j.kind)}
						<dt>Started by</dt>
						<dd>
							<a href={pol.href}>{pol.label}</a>
							<span class="muted"
								>{j.origin === 'scheduled'
									? 'on its schedule'
									: 'run by hand'}</span
							>
						</dd>
					{/if}
					{#if j.error}
						<dt>Error class</dt>
						<dd class="mono">{j.error.class}</dd>
					{/if}
					<dt>Job ID</dt>
					<dd class="id">
						<span class="mono">{j.id}</span><CopyButton value={j.id} what="job ID" />
					</dd>
				</dl>
			</Card>

			<Card title="Targets and locks">
				{#if j.targets.length}
					<ul class="list" role="list" aria-label="Targets">
						{#each j.targets as t (t.type + t.id)}
							<li>
								<span class="mono">{targetText(t.type, t.id, nameOf)}</span>
								{#if t.environmentId && t.environmentId !== j.environmentId}<span
										class="muted">on {envName(t.environmentId)}</span
									>{/if}
							</li>
						{/each}
					</ul>
				{:else}<p class="muted">No specific targets.</p>{/if}
				{#if j.locks.length}
					<h3 class="sub">Locks {j.locksHeld ? '(held)' : '(not held now)'}</h3>
					<ul class="list" role="list" aria-label="Locks">
						{#each j.locks as l, i (i)}
							<li>
								<span class="mono"
									>{l.scope}{l.name
										? ` ${l.name === '*' ? '(all)' : l.name}`
										: ''}</span
								>
								<Badge tone={l.mode === 'exclusive' ? 'warn' : 'neutral'}
									>{l.mode === 'exclusive' ? 'Exclusive' : 'Shared'}</Badge
								>
							</li>
						{/each}
					</ul>
				{/if}
			</Card>

			<Card title="Timeline">
				<ol class="timeline">
					{#each timeline as t (t.label)}
						<li>
							<span>{t.label}</span><time datetime={t.at} class="num"
								>{formatDateTime(t.at!)}</time
							>
						</li>
					{/each}
				</ol>
			</Card>
		</div>
	</div>

	<ConfirmDialog
		bind:open={cancelOpen}
		title="Cancel {jobKindLabel(j.kind).toLowerCase()}"
		message="The job stops at its next safe point."
		consequences={[
			'Steps that already finished are not undone.',
			'Cleanup steps still run, for example restarting containers stopped for a backup.'
		]}
		confirmLabel="Cancel job"
		cancelLabel="Keep running"
		tone="danger"
		onconfirm={cancel}
	/>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 360px), 1fr));
		gap: var(--space-4);
		align-items: start;
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
		min-width: 0;
		margin: 0;
		overflow-wrap: anywhere;
	}

	.id {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}

	.list {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.list li {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.sub {
		margin: var(--space-4) 0 var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-body);
		font-weight: var(--weight-medium);
	}

	.timeline {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.timeline li {
		display: flex;
		justify-content: space-between;
		gap: var(--space-3);
	}

	.timeline span {
		color: var(--text-muted);
	}
</style>
