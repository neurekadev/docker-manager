<script lang="ts">
	// Update policy detail (#20): candidates with running and registry
	// digests, quarantined digests with the manual recovery (pin @sha256 in
	// your own source), schedules, window and the applied digest history.
	// Check runs a digest check (never pulls); Preview update shows what a
	// run would do and applies it.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Ban from '@lucide/svelte/icons/ban';
	import CircleArrowUp from '@lucide/svelte/icons/circle-arrow-up';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import Pencil from '@lucide/svelte/icons/pencil';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Layers from '@lucide/svelte/icons/layers';
	import Server from '@lucide/svelte/icons/server';
	import { api, unwrap, unwrapEmpty, type Job } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		DestructiveConfirm,
		EmptyState,
		IconButton,
		JobProgress,
		KpiCard,
		Menu,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Table,
		formatDateTime,
		formatRelative,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import {
		environmentName,
		ifMatch,
		newIdempotencyKey,
		stacksQuery
	} from '$lib/features/common/data';
	import Digest from '$lib/features/common/Digest.svelte';
	import { actionError } from '$lib/features/common/errors';
	import Columns from '$lib/features/common/Columns.svelte';
	import Facts from '$lib/features/common/Facts.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import CandidatesTable from '$lib/features/updates/CandidatesTable.svelte';
	import UpdatePreviewDialog from '$lib/features/updates/UpdatePreviewDialog.svelte';
	import {
		recoveryText,
		summaryText,
		windowText,
		type UpdateCandidate,
		type UpdatePolicy
	} from '$lib/features/updates/model';
	import { candidatesQuery, updateKeys, updatePolicyQuery } from '$lib/features/updates/queries';

	type History = NonNullable<UpdatePolicy['recentHistory']>[number];

	const id = $derived(page.params.policyId ?? '');
	const qc = useQueryClient();
	const policy = createQuery(() => updatePolicyQuery(id));
	const candidates = createQuery(() => candidatesQuery(id));
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());

	const p = $derived(policy.data);
	const env = $derived(envs.data?.find((e) => e.id === p?.environmentId));
	const stack = $derived(
		p?.target.type === 'stack' ? stacks.data?.find((s) => s.id === p.target.id) : undefined
	);
	const targetLabel = $derived(
		p
			? p.target.type === 'stack'
				? stack?.displayName || stack?.name || 'Stack'
				: p.target.id
			: ''
	);

	usePage(() => ({
		title: p?.name ?? 'Update policy',
		crumbs: [
			{ label: 'Updates', href: routes.updates() },
			{ label: p?.name ?? 'Update policy' }
		]
	}));

	let checkJob = $state<Job | null>(null);
	let checking = $state(false);
	let actionErr = $state<string | null>(null);
	let previewOpen = $state(false);
	let deleteOpen = $state(false);

	async function check() {
		if (!p) return;
		checking = true;
		actionErr = null;
		try {
			checkJob = await unwrap(
				api.POST('/api/v1/update-policies/{policyId}/checks', {
					params: {
						path: { policyId: p.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					}
				})
			);
		} catch (e) {
			actionErr = actionError(e);
		} finally {
			checking = false;
		}
	}

	function checked(j: Job) {
		void qc.invalidateQueries({ queryKey: updateKeys.detail(id) });
		void qc.invalidateQueries({ queryKey: ['policies', 'list'] });
		if (j.state === 'succeeded')
			toast.success(`Checked ${p?.name ?? 'the policy'} for updates`);
		else
			toast.error('The check did not finish', {
				body: j.error?.recovery ?? j.error?.message
			});
	}

	async function remove() {
		if (!p) return;
		await unwrapEmpty(
			api.DELETE('/api/v1/update-policies/{policyId}', {
				params: { path: { policyId: p.id }, header: { 'If-Match': ifMatch(p.revision) } }
			})
		);
		toast.success(`Deleted update policy ${p.name}`);
		await qc.invalidateQueries({ queryKey: ['policies', 'list'] });
		await goto(routes.updates());
	}

	const menu = $derived.by<MenuEntry[]>(() => {
		const items: MenuEntry[] = [];
		if (!p) return items;
		if (has(p, 'update_policy.manage')) {
			items.push({ label: 'Edit policy', icon: Pencil, href: routes.updatePolicyEdit(p.id) });
			items.push({ separator: true });
			items.push({
				label: 'Delete policy',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleteOpen = true)
			});
		}
		return items;
	});

	const quarantined = $derived(
		(candidates.data ?? []).filter(
			(c) => c.status === 'quarantined' || c.status === 'run_failed'
		)
	);

	const historyColumns: Column<History>[] = [
		{
			id: 'at',
			header: 'When',
			cell: atCell,
			sortValue: (h) => h.at,
			width: '170px',
			stack: 'meta'
		},
		{
			id: 'service',
			header: 'Service',
			cell: svcCell,
			sortValue: (h) => h.service,
			stack: 'title'
		},
		{ id: 'outcome', header: 'Outcome', cell: outcomeCell, width: '140px', stack: 'status' },
		{ id: 'digests', header: 'Digest', cell: digestsCell },
		{ id: 'job', header: 'Job', cell: jobCell, width: '90px' }
	];
	const OUTCOME: Record<
		History['outcome'],
		{ tone: 'ok' | 'neutral' | 'danger'; label: string }
	> = {
		updated: { tone: 'ok', label: 'Updated' },
		unchanged: { tone: 'neutral', label: 'Unchanged' },
		kept_stopped: { tone: 'neutral', label: 'Kept stopped' },
		failed: { tone: 'danger', label: 'Failed' }
	};
</script>

{#snippet atCell(h: History)}<span class="num">{formatDateTime(h.at)}</span>{/snippet}
{#snippet svcCell(h: History)}{h.service}{/snippet}
{#snippet outcomeCell(h: History)}
	<Badge tone={OUTCOME[h.outcome].tone} dot>{OUTCOME[h.outcome].label}</Badge>
{/snippet}
{#snippet digestsCell(h: History)}
	<span class="pair">
		<Digest value={h.fromDigest} copy={false} />
		<span class="muted" aria-hidden="true">→</span>
		<span class="sr-only">to</span>
		<Digest value={h.toDigest} copy={false} />
	</span>
{/snippet}
{#snippet jobCell(h: History)}
	{#if h.jobId}<a href={routes.job(h.jobId)}>Details</a>{:else}<span class="muted">—</span>{/if}
{/snippet}

<Page>
	<QueryView
		query={policy}
		errorTitle="The update policy could not be loaded."
		notFoundTitle="This update policy does not exist."
		notFoundDescription="It was deleted, or you no longer have access to it."
	>
		{#snippet children(p: UpdatePolicy)}
			{@const s = summaryText(p)}
			<PageHeader
				title={p.name}
				icon={PackageCheck}
				color="violet"
				description="Follows the digests behind the tags {p.target.type === 'stack'
					? 'of a stack'
					: 'of a DockYard-managed container'}. Your Compose files and tags never change."
				meta={[
					{ icon: p.target.type === 'stack' ? Layers : PackageCheck, label: targetLabel },
					{ icon: Server, label: environmentName(envs.data, p.environmentId) },
					{
						label: p.summary?.lastCheckAt
							? `Checked ${formatRelative(p.summary.lastCheckAt)}`
							: 'Never checked'
					}
				]}
			>
				{#snippet status()}<Badge tone={s.tone} dot>{s.text}</Badge>{/snippet}
				{#snippet actions()}
					{#if has(p, 'update.check')}
						<Button
							icon={RefreshCw}
							loading={checking}
							onclick={check}
							disabled={!!checkJob && !checkJob.finishedAt}>Check for updates</Button
						>
					{/if}
					{#if has(p, 'update.check') || has(p, 'update.run')}
						<Button variant="primary" onclick={() => (previewOpen = true)}
							>Preview update</Button
						>
					{/if}
					{#if menu.length}
						<Menu items={menu} label="More actions for {p.name}">
							{#snippet trigger(props)}
								<IconButton
									{...props}
									label="More actions"
									icon={Ellipsis}
									variant="secondary"
								/>
							{/snippet}
						</Menu>
					{/if}
				{/snippet}
			</PageHeader>

			{#if env && !env.online}
				<OfflineEnvironment name={env.name} since={env.connectionChangedAt} />
			{/if}
			{#if actionErr}
				<Notice tone="danger" title="The check did not start" live="alert"
					>{actionErr}</Notice
				>
			{/if}
			{#if checkJob}
				<JobProgress
					jobId={checkJob.id}
					title="Check {p.name} for updates"
					onfinish={checked}
				/>
			{/if}

			<KpiRow>
				<KpiCard
					label="Updates available"
					icon={CircleArrowUp}
					color="violet"
					value={String(p.summary?.available ?? 0)}
					tone={p.summary?.available ? 'warn' : undefined}
					secondary="Newer digest behind the tag"
				/>
				<KpiCard
					label="Up to date"
					icon={CircleCheck}
					color="green"
					value={String(p.summary?.upToDate ?? 0)}
					secondary="Running the registry's digest"
				/>
				<KpiCard
					label="Not eligible"
					icon={Ban}
					color="slate"
					value={String(p.summary?.ineligible ?? 0)}
					secondary="Pinned, built or excluded"
				/>
				<KpiCard
					label="Quarantined or failed"
					icon={ShieldAlert}
					color="rose"
					value={String((p.summary?.quarantined ?? 0) + (p.summary?.failed ?? 0))}
					tone={(p.summary?.quarantined ?? 0) + (p.summary?.failed ?? 0)
						? 'danger'
						: undefined}
					secondary="Never retried automatically"
				/>
			</KpiRow>

			<Card title="Services" padding="none">
				<QueryView query={candidates} errorTitle="The candidates could not be loaded.">
					{#snippet children(rows: UpdateCandidate[])}
						{#if rows.length}
							<CandidatesTable
								candidates={rows}
								label="Update candidates of {p.name}"
							/>
						{:else}
							<EmptyState
								icon={PackageCheck}
								color="violet"
								title="Nothing checked yet."
								description="Check for updates to compare each tag's digest in the registry with what runs on the host."
								level={3}
								compact
							/>
						{/if}
					{/snippet}
				</QueryView>
			</Card>

			{#if quarantined.length}
				<Card title="Quarantined digests">
					<Notice
						tone="danger"
						icon={ShieldAlert}
						title="Failed updates are not rolled back"
						live="none"
					>
						DockYard will not apply these digests again. To go back to a working image,
						pin it by digest in your own Compose file and deploy the stack.
					</Notice>
					<ul class="quarantine" role="list">
						{#each quarantined as c (c.id)}
							<li>
								<div class="q-head">
									<strong>{c.service}</strong>
									<Digest value={c.candidateDigest} tone="danger" />
									{#if c.errorClass}<Badge tone="danger"
											>{c.errorClass.replaceAll('_', ' ')}</Badge
										>{/if}
								</div>
								<p>{recoveryText(c)}</p>
							</li>
						{/each}
					</ul>
				</Card>
			{/if}

			<Columns>
				<Card title="Schedules">
					<Facts
						columns={1}
						items={[
							{ label: 'Checks', render: checkSched },
							{ label: 'Automatic updates', render: runSched },
							{ label: 'Update window', value: windowText(p.window) },
							{
								label: 'Health wait',
								value: p.waitTimeoutSeconds
									? `${p.waitTimeoutSeconds} s`
									: 'Default'
							},
							{
								label: 'Services',
								value: p.excludeServices?.length
									? `All except ${p.excludeServices.join(', ')}`
									: p.services?.length
										? p.services.join(', ')
										: 'All services'
							}
						]}
					/>
				</Card>
				<Card title="Update history" padding="none">
					{#if p.recentHistory?.length}
						<Table
							label="Update history of {p.name}"
							rows={p.recentHistory}
							columns={historyColumns}
							rowKey={(h) => `${h.at}-${h.service}`}
							sort={{ column: 'at', direction: 'desc' }}
						/>
					{:else}
						<EmptyState
							icon={PackageCheck}
							color="slate"
							title="No updates applied yet."
							description="Applied and failed updates appear here with their digests."
							level={3}
							compact
						/>
					{/if}
				</Card>
			</Columns>

			{#snippet checkSched()}
				{#if p.checkSchedule}<ScheduleSummary {...p.checkSchedule} />{/if}
			{/snippet}
			{#snippet runSched()}
				{#if p.runSchedule}<ScheduleSummary {...p.runSchedule} />{/if}
			{/snippet}

			<UpdatePreviewDialog bind:open={previewOpen} policy={p} canRun={has(p, 'update.run')} />
			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Delete update policy {p.name}"
				consequences={[
					'Removes the policy, its candidates, quarantine list and update history.',
					'Nothing is checked or updated for this target afterwards.',
					'Containers, images and your Compose files are not touched; the audit log and job history stay.'
				]}
				confirmText={p.name}
				confirmLabel="Delete policy"
				onconfirm={remove}
			/>
		{/snippet}
	</QueryView>
</Page>

<style>
	.pair {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
	}

	.quarantine {
		display: grid;
		gap: var(--space-3);
		margin-top: var(--space-3);
	}

	.quarantine li {
		padding-top: var(--space-3);
		border-top: 1px solid var(--border-subtle);
		overflow-wrap: anywhere;
	}

	.q-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin-bottom: var(--space-1);
	}

	.q-head strong {
		color: var(--text-strong);
	}
</style>
