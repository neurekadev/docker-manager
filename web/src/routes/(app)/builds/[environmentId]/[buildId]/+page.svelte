<script lang="ts">
	// One build (#33): its source, the exact commit it built (resolved from
	// the ref before the build), the result (image, duration or the error)
	// and the live BuildKit log. A running build can be cancelled.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Clock from '@lucide/svelte/icons/clock';
	import GitCommitHorizontal from '@lucide/svelte/icons/git-commit-horizontal';
	import Hammer from '@lucide/svelte/icons/hammer';
	import Server from '@lucide/svelte/icons/server';
	import Square from '@lucide/svelte/icons/square';
	import { ApiRequestError, api, unwrap, type Job } from '$lib/api/client';
	import type { JobWatcher } from '$lib/api/jobs.svelte';
	import { imageBuildQuery, queryKeys } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		ConfirmDialog,
		CopyButton,
		EmptyState,
		ErrorState,
		Notice,
		PageHeader,
		Skeleton,
		StatusBadge,
		formatRelative,
		toast,
		type MetaItem
	} from '$lib/ui';
	import BuildLog from '$lib/features/builds/BuildLog.svelte';
	import { repoLabel } from '$lib/features/builds/source';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import { compactDuration } from '$lib/features/resources/model';
	import { sentence } from '$lib/features/resources/refusals';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	const env = $derived(page.params.environmentId ?? '');
	const id = $derived(page.params.buildId ?? '');
	const queryClient = useQueryClient();
	const scope = useEnvironmentScope();
	const envName = $derived(scope.name(env));
	const q = createQuery(() => ({
		...imageBuildQuery(env, id),
		refetchInterval: (query) =>
			query.state.data &&
			(query.state.data.status === 'queued' || query.state.data.status === 'running')
				? 5000
				: false
	}));
	const b = $derived(q.data);
	let watcher = $state<JobWatcher | null>(null);
	let cancelOpen = $state(false);

	const title = $derived(b?.tags[0] ?? 'Build');
	usePage(() => ({
		title,
		crumbs: [{ label: 'Builds', href: routes.builds() }, { label: envName }, { label: title }]
	}));

	const notFound = $derived(q.error instanceof ApiRequestError && q.error.status === 404);
	const running = $derived(!!b && (b.status === 'queued' || b.status === 'running'));
	const cancellable = $derived(
		running && (watcher?.job?.cancellable ?? true) && !watcher?.job?.cancelRequested
	);

	function finished(j: Job) {
		void queryClient.invalidateQueries({ queryKey: queryKeys.images.all });
		if (j.state === 'succeeded') toast.success(`Built ${title}`);
	}

	async function cancel() {
		await unwrap(
			api.POST('/api/v1/jobs/{jobId}/cancellations', {
				params: { path: { jobId: b!.jobId } }
			})
		);
		toast.info(`Cancelling the build of ${title}`);
	}

	const meta = $derived<MetaItem[]>(
		b
			? [
					{ icon: Server, label: envName },
					{
						icon: Clock,
						label: `Started ${formatRelative(b.startedAt ?? b.createdAt)}`,
						title: b.startedAt ?? b.createdAt
					},
					...(b.resolvedCommit
						? [
								{
									icon: GitCommitHorizontal,
									label: b.resolvedCommit.slice(0, 12),
									mono: true,
									title: b.resolvedCommit
								}
							]
						: [])
				]
			: []
	);
	const source = $derived<Fact[]>(
		b
			? [
					{
						label: 'Repository',
						value: repoLabel(b.gitUrl),
						mono: true,
						title: b.gitUrl
					},
					{ label: 'Ref', value: b.ref || 'Default branch', mono: !!b.ref },
					{ label: 'Resolved to', value: b.resolvedRef, mono: true },
					{ label: 'Commit', value: b.resolvedCommit, mono: true },
					{
						label: 'Context',
						value: b.contextPath || 'Repository root',
						mono: !!b.contextPath
					},
					{ label: 'Dockerfile', value: b.dockerfile || 'Dockerfile', mono: true },
					{ label: 'Target stage', value: b.target, mono: true },
					{
						label: 'Platform',
						value: b.platform || "The environment's platform",
						mono: !!b.platform
					},
					{
						label: 'Build arguments',
						value: b.buildArgNames.join(', '),
						mono: true,
						note: b.buildArgNames.length ? 'names only' : undefined
					},
					{
						label: 'Options',
						value:
							[b.noCache && 'no cache', b.pull && 'pulled base images']
								.filter(Boolean)
								.join(', ') || 'Defaults'
					}
				]
			: []
	);
	const result = $derived<Fact[]>(
		b
			? [
					{ label: 'Image names', value: b.tags.join(', '), mono: true },
					{
						label: 'Image ID',
						value: b.imageId,
						mono: true,
						href: b.imageId ? routes.image(env, b.imageId) : undefined
					},
					{
						label: 'Duration',
						value:
							b.durationMs !== undefined
								? compactDuration(b.durationMs)
								: running
									? 'Running'
									: undefined
					},
					{
						label: 'Finished',
						value: b.finishedAt ? formatRelative(b.finishedAt) : undefined,
						title: b.finishedAt
					}
				]
			: []
	);
</script>

<Page>
	{#if q.isPending}
		<div class="loading" aria-busy="true">
			<Skeleton height="56px" radius="lg" /><Skeleton lines={5} height="20px" />
		</div>
	{:else if notFound}
		<EmptyState
			icon={Hammer}
			color="slate"
			title="No such build on {envName}."
			description="It may belong to another environment."
			level={1}
		>
			{#snippet actions()}<Button variant="secondary" href={routes.builds()}
					>Back to builds</Button
				>{/snippet}
		</EmptyState>
	{:else if q.isError}
		<ErrorState
			error={q.error}
			title="The build could not be loaded."
			onretry={() => q.refetch()}
		/>
	{:else if b}
		<PageHeader
			{title}
			description="Built from {repoLabel(b.gitUrl)}"
			icon={Hammer}
			color="violet"
			{meta}
		>
			{#snippet status()}<StatusBadge status={b.status} kind="job" />{/snippet}
			{#snippet actions()}
				{#if b.resolvedCommit}<CopyButton
						value={b.resolvedCommit}
						what="commit SHA"
						text
					/>{/if}
				{#if cancellable}
					<Button variant="danger-soft" icon={Square} onclick={() => (cancelOpen = true)}
						>Cancel build</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if b.status === 'failed' || b.status === 'interrupted'}
			<Notice
				tone="danger"
				title="The build {b.status === 'failed' ? 'failed' : 'was interrupted'}"
				live="none"
			>
				{b.errorMessage ? sentence(b.errorMessage) : 'See the log below.'}
				{#if b.errorClass}<span class="muted"> ({b.errorClass})</span>{/if}
			</Notice>
		{:else if b.status === 'cancelled'}
			<Notice tone="info" title="The build was cancelled" live="none"
				>Nothing was tagged.</Notice
			>
		{/if}

		<div class="grid">
			<Card title="Source"><Facts items={source} label="Source of the build" /></Card>
			<Card title="Result"><Facts items={result} label="Result of the build" /></Card>
		</div>
		<Card title="Log">
			{#key b.jobId}<BuildLog
					jobId={b.jobId}
					onwatcher={(x) => (watcher = x)}
					onfinish={finished}
				/>{/key}
		</Card>

		<ConfirmDialog
			bind:open={cancelOpen}
			title="Cancel the build of {title}?"
			consequences={[
				'BuildKit stops at the next safe point; nothing is tagged.',
				'Layers built so far stay in the build cache.'
			]}
			confirmLabel="Cancel build"
			cancelLabel="Keep building"
			tone="danger"
			onconfirm={cancel}
		/>
	{/if}
</Page>

<style>
	.loading {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-4);
	}

	@media (max-width: 1023px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}
</style>
