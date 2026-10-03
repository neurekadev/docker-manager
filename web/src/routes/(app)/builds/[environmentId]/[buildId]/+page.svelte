<script lang="ts">
	// One build (#33): its source, the exact commit it built (resolved from
	// the ref before the build), the result (image, duration or the error in
	// words) and the live BuildKit log. A running build can be cancelled; a
	// finished one built again with the same inputs ("Build Again": its
	// definition's run, else the same source; with build arguments, whose
	// values are never kept, the prefilled form) or saved as a definition.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Clock from '@lucide/svelte/icons/clock';
	import FileCode from '@lucide/svelte/icons/file-code';
	import GitCommitHorizontal from '@lucide/svelte/icons/git-commit-horizontal';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Server from '@lucide/svelte/icons/server';
	import Square from '@lucide/svelte/icons/square';
	import { ApiRequestError, api, unwrap, type Job } from '$lib/api/client';
	import type { JobWatcher } from '$lib/api/jobs.svelte';
	import { goto } from '$app/navigation';
	import { imageBuildQuery, queryKeys } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
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
		errorMessage,
		formatDateTime,
		formatDuration,
		formatRelative,
		toast,
		type MetaItem
	} from '$lib/ui';
	import Columns from '$lib/features/common/Columns.svelte';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import DefinitionDialog from '$lib/features/builds/DefinitionDialog.svelte';
	import BuildLog from '$lib/features/builds/BuildLog.svelte';
	import { repoLabel, sourceOfBuild } from '$lib/features/builds/source';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import { idempotencyKey } from '$lib/features/resources/jobs.svelte';
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
	let saveOpen = $state(false);
	let again = $state(false);
	const canBuild = $derived(scope.can('image.build', env));
	const canSave = $derived(scope.can('build_definition.manage', env));
	const duration = (ms: number | undefined) =>
		ms === undefined ? undefined : formatDuration(ms / 1000);

	/**
	 * Build again with the same inputs: the definition's run (it keeps the
	 * argument values), else the same source; a build with arguments opens
	 * the prefilled form, as their values are never kept.
	 */
	async function buildAgain() {
		if (!b) return;
		const { source, argsMissing } = sourceOfBuild(b);
		if (!b.definitionId && argsMissing) {
			await goto(routes.newBuild(env, undefined, b.id));
			return;
		}
		again = true;
		try {
			const job = b.definitionId
				? await unwrap(
						api.POST(
							'/api/v1/environments/{environmentId}/build-definitions/{definitionId}/runs',
							{
								params: {
									path: { environmentId: env, definitionId: b.definitionId },
									header: { 'Idempotency-Key': idempotencyKey() }
								}
							}
						)
					)
				: await unwrap(
						api.POST('/api/v1/environments/{environmentId}/images/builds', {
							params: {
								path: { environmentId: env },
								header: { 'Idempotency-Key': idempotencyKey() }
							},
							body: source
						})
					);
			toast.info(`Building ${title} again`);
			void queryClient.invalidateQueries({ queryKey: queryKeys.images.all });
			await goto(routes.build(env, job.id));
		} catch (e) {
			toast.error(`${title} couldn't be built again.`, { body: errorMessage(e) });
		} finally {
			again = false;
		}
	}

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
						title: formatDateTime(b.startedAt ?? b.createdAt)
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
					{ label: 'Resolved To', value: b.resolvedRef, mono: true },
					{ label: 'Commit', value: b.resolvedCommit, mono: true },
					{
						label: 'Context',
						value: b.contextPath || 'Repository root',
						mono: !!b.contextPath
					},
					{ label: 'Dockerfile', value: b.dockerfile || 'Dockerfile', mono: true },
					{ label: 'Target Stage', value: b.target, mono: true },
					{
						label: 'Platform',
						value: b.platform || "The environment's platform",
						mono: !!b.platform
					},
					{
						label: 'Build Arguments',
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
					{ label: 'Image Names', value: b.tags.join(', '), mono: true },
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
								? duration(b.durationMs)
								: running
									? 'Running'
									: undefined
					},
					{
						label: 'Finished',
						value: b.finishedAt ? formatRelative(b.finishedAt) : undefined,
						title: b.finishedAt ? formatDateTime(b.finishedAt) : undefined
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
			icon={resourceIcon('build').icon}
			color="slate"
			title="No such build on {envName}."
			description="It may belong to another environment."
			level={1}
		>
			{#snippet actions()}<Button variant="secondary" href={routes.builds()}
					>Back to Builds</Button
				>{/snippet}
		</EmptyState>
	{:else if q.isError}
		<ErrorState
			error={q.error}
			title="The build could not be loaded."
			onretry={() => q.refetch()}
		/>
	{:else if b}
		<PageHeader {title} {...resourceIcon('build')} {meta}>
			{#snippet status()}<StatusBadge status={b.status} kind="job" />{/snippet}
			{#snippet actions()}
				{#if canBuild && !running}
					<Button variant="primary" icon={RotateCw} loading={again} onclick={buildAgain}
						>Build Again</Button
					>
				{/if}
				{#if canSave && !b.definitionId}
					<Button icon={FileCode} onclick={() => (saveOpen = true)}
						>Save as Definition</Button
					>
				{/if}
				{#if b.resolvedCommit}<CopyButton
						value={b.resolvedCommit}
						what="commit SHA"
						text
					/>{/if}
				{#if cancellable}
					<Button variant="danger-soft" icon={Square} onclick={() => (cancelOpen = true)}
						>Cancel Build</Button
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
				{#if b.errorClass}
					<Disclosure summary="Details">
						<span class="muted"
							>Error Class: <span class="mono">{b.errorClass}</span></span
						>
					</Disclosure>
				{/if}
			</Notice>
		{:else if b.status === 'cancelled'}
			<Notice tone="info" title="The build was cancelled" live="none"
				>Nothing was tagged.</Notice
			>
		{/if}

		<Columns ratio="equal">
			<Card title="Source"><Facts items={source} label="Source of the Build" /></Card>
			<Card title="Result"><Facts items={result} label="Result of the Build" /></Card>
		</Columns>
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
			confirmLabel="Cancel Build"
			cancelLabel="Keep Building"
			tone="danger"
			onconfirm={cancel}
		/>
		{#if saveOpen}
			<DefinitionDialog
				bind:open={saveOpen}
				environments={scope.targets.filter((t) => t.id === env)}
				environmentId={env}
				source={sourceOfBuild(b).source}
			/>
		{/if}
	{/if}
</Page>

<style>
	.loading {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}
</style>
