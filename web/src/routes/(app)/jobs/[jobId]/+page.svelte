<script lang="ts">
	// Job detail (#26): what the job did and on what first (the target's
	// name as title, a plain summary, linked targets), its state once (in
	// the header), a failure as a headline in words with the engine's
	// message below it and where to try again, live progress (JobProgress
	// follows the job's event stream: items and messages), the timeline to
	// the second, and the technical detail (kind, error class, locks,
	// attempt, IDs) behind "Advanced". Cancel while the job can still stop
	// at a safe point. "Try Again" starts a retry (POST /jobs/{id}/retries)
	// when the server offers one (`retryable`) and opens the new job; other
	// failed jobs link to the page of the action that started them. A retry
	// names the job it retries ("Retry Of").
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Activity from '@lucide/svelte/icons/activity';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import CircleStop from '@lucide/svelte/icons/circle-stop';
	import Clock from '@lucide/svelte/icons/clock';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Server from '@lucide/svelte/icons/server';
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
		jobErrorHeadline,
		jobHeadline,
		jobKindLabel,
		jobKindPhrase,
		jobRetry,
		policyPage,
		RETRY_ERRORS,
		stackNames,
		targetHref,
		targetName,
		targetText,
		timelineTime
	} from '$lib/features/jobs/labels';
	import { liveKeys } from '$lib/live/keys';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
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
		statusInfo,
		toast,
		type MetaItem
	} from '$lib/ui';
	import { newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Columns from '$lib/features/common/Columns.svelte';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import Page from '$lib/features/common/Page.svelte';

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
		envId ? (envs.data?.find((e) => e.id === envId)?.name ?? 'Unknown environment') : undefined;

	// Jobs without a nameable target (a prune run, an environment
	// migration) name their environment, as in the jobs list.
	const headline = $derived(
		j
			? jobHeadline(j, {
					nameOf,
					fallback: envs.data?.find((e) => e.id === j.environmentId)?.name
				})
			: { title: 'Job', subtitle: '' }
	);
	const title = $derived(headline.title);
	usePage(() => ({
		title: headline.subtitle ? `${headline.title} — ${headline.subtitle}` : headline.title,
		crumbs: [
			{ label: 'Jobs', href: routes.jobs() },
			{
				label: j
					? headline.subtitle
						? `${headline.title} — ${headline.subtitle}`
						: headline.title
					: '…'
			}
		]
	}));

	const failed = $derived(
		!!j && ['failed', 'partial', 'interrupted', 'cancelled'].includes(j.state)
	);
	const again = $derived(j ? jobRetry(j) : null);

	/** "Check for updates, started on its schedule. It succeeded after 1 s." */
	const summary = $derived.by(() => {
		if (!j) return '';
		const what = jobKindPhrase(j.kind);
		const how =
			j.origin === 'scheduled'
				? 'started on its schedule'
				: j.origin === 'api_token'
					? 'started with an API token'
					: 'started by hand';
		const took = jobDuration(j);
		const state = statusInfo(j.state, 'job').label.toLowerCase();
		const end = jobActive(j.state)
			? ` It is ${state}.`
			: ` It ${j.state === 'cancelled' ? 'was cancelled' : state}${took ? ` after ${took}` : ''}.`;
		return `${what}, ${how}.${end}`;
	});

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

	let retrying = $state(false);
	/** Starts the job again as a new job and opens it. */
	async function retry() {
		if (retrying) return;
		retrying = true;
		const what = title;
		try {
			const out = await unwrap(
				api.POST('/api/v1/jobs/{jobId}/retries', {
					params: {
						path: { jobId: id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					}
				})
			);
			void qc.invalidateQueries({ queryKey: liveKeys.list('jobs') });
			toast.success(`Retried ${what}`, { body: 'This page now follows the new job.' });
			await goto(routes.job(out.id));
		} catch (e) {
			toast.error(`${what} was not retried`, { body: actionError(e, RETRY_ERRORS) });
			void job.refetch();
		} finally {
			retrying = false;
		}
	}

	const timeline = $derived(
		j
			? [
					{ label: 'Queued', at: j.createdAt },
					{ label: 'Sent to Its Runner', at: j.dispatchedAt },
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
	<Page>
		<div class="loading" aria-busy="true">
			<Skeleton height="72px" radius="lg" />
			<Skeleton height="200px" radius="lg" />
		</div>
	</Page>
{:else}
	<Page>
		<PageHeader {title} description={summary} {...resourceIcon('job')} {meta}>
			{#snippet status()}<StatusBadge status={j.state} kind="job" />{/snippet}
			{#snippet actions()}
				{#if again === 'retry'}
					<Button variant="primary" icon={RotateCw} loading={retrying} onclick={retry}
						>Try Again</Button
					>
				{:else if again}
					<Button variant="primary" icon={RotateCw} href={again.href}
						>{again.label}</Button
					>
				{/if}
				{#if j.cancellable && !j.cancelRequested && jobActive(j.state)}
					<Button
						variant="danger-soft"
						icon={CircleStop}
						onclick={() => (cancelOpen = true)}>Cancel Job</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if j.error && failed}
			<Notice
				tone={j.state === 'cancelled' ? 'info' : j.state === 'partial' ? 'warn' : 'danger'}
				title={jobErrorHeadline(j.error, j.state)}
				live="none"
			>
				{#if j.error.recovery}<p>{j.error.recovery}</p>{/if}
				{#if j.error.message}<p class="raw mono">{j.error.message}</p>{/if}
			</Notice>
		{/if}
		{#if j.blockedBy}
			<Notice tone="warn" title="Waiting" live="none">
				{blockedText(j.blockedBy)}
				{#if j.blockedBy.jobId}
					<a href={routes.job(j.blockedBy.jobId)}>Open the job it waits for</a>.
				{/if}
			</Notice>
		{/if}
		{#if j.cancelRequested && jobActive(j.state)}
			<Notice tone="info" title="Cancellation Requested" live="status">
				It stops at the next safe point; cleanup steps still run.
			</Notice>
		{/if}

		{#key id}
			<JobProgress
				jobId={id}
				{title}
				variant="panel"
				summary={false}
				notices={null}
				onfinish={() => job.refetch()}
			/>
		{/key}

		<Columns ratio="equal">
			<Card title="Details">
				<dl class="facts">
					<dt>What</dt>
					<dd>{jobKindLabel(j.kind)}</dd>
					<dt>{j.targets.length === 1 ? 'Target' : 'Targets'}</dt>
					<dd>
						{#if j.targets.length}
							<ul class="list" role="list" aria-label="Targets">
								{#each j.targets as t (t.type + t.id)}
									{@const href = targetHref(t, j.environmentId)}
									<li>
										{#if href}<a {href}>{targetName(t.type, t.id, nameOf)}</a
											>{:else}<span>{targetText(t.type, t.id, nameOf)}</span
											>{/if}
										{#if t.environmentId && t.environmentId !== j.environmentId}<span
												class="muted">on {envName(t.environmentId)}</span
											>{/if}
									</li>
								{/each}
							</ul>
						{:else}<span class="muted">Nothing specific</span>{/if}
					</dd>
					{#if j.environmentId}
						<dt>Environment</dt>
						<dd>
							<a href={routes.environment(j.environmentId)}
								>{envName(j.environmentId)}</a
							>
						</dd>
					{/if}
					<dt>Started By</dt>
					<dd>
						{#if j.policyId}
							{@const pol = policyPage(j.kind, j.policyId)}
							<a href={pol.href}>{pol.label}</a>
							<span class="muted"
								>{j.origin === 'scheduled'
									? 'on its schedule'
									: 'run by hand'}</span
							>
						{:else}
							{ORIGIN_LABELS[j.origin] ?? j.origin}
						{/if}
					</dd>
					{#if j.retryOf}
						<dt>Retry Of</dt>
						<dd><a href={routes.job(j.retryOf)}>The earlier job</a></dd>
					{/if}
					<dt>Duration</dt>
					<dd class="num">{jobDuration(j) || '—'}</dd>
				</dl>
			</Card>

			<Card title="Timeline">
				<ol class="timeline">
					{#each timeline as t (t.label)}
						<li>
							<span>{t.label}</span><time datetime={t.at} class="num"
								>{timelineTime(t.at)}</time
							>
						</li>
					{/each}
				</ol>
			</Card>
		</Columns>

		<Disclosure summary="Advanced">
			<Card>
				<dl class="facts">
					<dt>Kind</dt>
					<dd class="mono">{j.kind}</dd>
					<dt>Runs On</dt>
					<dd>
						{j.executor === 'agent' ? 'The environment’s agent' : 'Docker Manager'}
					</dd>
					<dt>Attempt</dt>
					<dd class="num">
						{j.attempt}
						{#if j.attempt > 1}<span class="muted"
								>(resumed after an interruption or a lost connection)</span
							>{/if}
					</dd>
					{#if j.error}
						<dt>Error Class</dt>
						<dd class="mono">{j.error.class}</dd>
					{/if}
					{#if j.locks.length}
						<dt>Locks</dt>
						<dd>
							<ul class="list" role="list" aria-label="Locks">
								{#each j.locks as l, i (i)}
									<li>
										<span class="mono"
											>{l.scope}{l.name
												? ` ${l.name === '*' ? '(all)' : l.name}`
												: ''}</span
										>
										<Badge tone={l.mode === 'exclusive' ? 'warn' : 'neutral'}
											>{l.mode === 'exclusive'
												? 'Exclusive'
												: 'Shared'}</Badge
										>
									</li>
								{/each}
							</ul>
							<span class="muted small"
								>{j.locksHeld ? 'Held now' : 'Not held now'}</span
							>
						</dd>
					{/if}
					{#if j.targets.length}
						<dt>Target IDs</dt>
						<dd>
							<ul class="list" role="list">
								{#each j.targets as t (t.type + t.id)}
									<li class="mono">{t.type}:{t.id}</li>
								{/each}
							</ul>
						</dd>
					{/if}
					{#if j.policyId}
						<dt>Policy ID</dt>
						<dd class="mono">{j.policyId}</dd>
					{/if}
					<dt>Job ID</dt>
					<dd class="id">
						<span class="mono">{j.id}</span><CopyButton value={j.id} what="job ID" />
					</dd>
				</dl>
			</Card>
		</Disclosure>
	</Page>

	<ConfirmDialog
		bind:open={cancelOpen}
		title="Cancel {jobKindLabel(j.kind)}"
		message="The job stops at its next safe point."
		consequences={[
			'Steps that already finished are not undone.',
			'Cleanup steps still run, for example restarting containers stopped for a backup.'
		]}
		confirmLabel="Cancel Job"
		cancelLabel="Keep Running"
		tone="danger"
		onconfirm={cancel}
	/>
{/if}

<style>
	.loading {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
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
		gap: var(--space-1);
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

	.raw {
		margin-top: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
		overflow-wrap: anywhere;
	}

	.small {
		font-size: var(--text-caption);
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
