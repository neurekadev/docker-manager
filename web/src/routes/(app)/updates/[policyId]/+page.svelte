<script lang="ts">
	// Environment update policy detail (#20): the stacks and standalone
	// containers it covers with their candidate summary, schedules and
	// window. Check runs a digest check on every target (never pulls);
	// Preview updates shows what a run would do and applies it.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Layers from '@lucide/svelte/icons/layers';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import Pencil from '@lucide/svelte/icons/pencil';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Server from '@lucide/svelte/icons/server';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, unwrap, unwrapEmpty, type Schema } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		DestructiveConfirm,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		Notice,
		PageHeader,
		Skeleton,
		Table,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import {
		environmentName,
		ifMatch,
		newIdempotencyKey,
		stacksQuery
	} from '$lib/features/common/data';
	import Digest from '$lib/features/common/Digest.svelte';
	import { actionError } from '$lib/features/common/errors';
	import Facts from '$lib/features/common/Facts.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import {
		candidateStatus,
		reasonLabel,
		windowText,
		type UpdateCandidate
	} from '$lib/features/updates/model';
	import {
		environmentUpdateKeys,
		environmentUpdatePolicyQuery,
		type EnvironmentUpdatePolicy
	} from '$lib/features/updates/queries';

	type Preview = Schema<'EnvironmentPreviewOutputBody'>;
	type Target = Schema<'EnvironmentTarget'>;
	interface PreviewRow {
		key: string;
		target: string;
		drift: boolean;
		item: UpdateCandidate;
	}

	const id = $derived(page.params.policyId ?? '');
	const qc = useQueryClient();
	const policy = createQuery(() => environmentUpdatePolicyQuery(id));
	const perms = createQuery(() => myPermissionsQuery());
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());
	const targets = createQuery(() => ({
		queryKey: environmentUpdateKeys.targets(id),
		queryFn: async ({ signal }: { signal: AbortSignal }) =>
			(
				await unwrap(
					api.GET('/api/v1/environment-update-policies/{policyId}/targets', {
						params: { path: { policyId: id } },
						signal
					})
				)
			).items
	}));
	const manage = $derived(can(accessOf(perms.data), 'update_policy.manage'));

	let preview = $state<Preview | null>(null);
	let error = $state<unknown>(null);
	let checking = $state(false);
	let previewing = $state(false);
	let applying = $state(false);
	let deleteOpen = $state(false);

	usePage(() => ({
		title: policy.data?.name ?? 'Update policy',
		crumbs: [
			{ label: 'Updates', href: routes.updates() },
			{ label: policy.data?.name ?? 'Update policy' }
		]
	}));

	function scopeLabel(p: EnvironmentUpdatePolicy): string {
		return p.scope === 'all' ? 'All environments' : environmentName(envs.data, p.environmentId);
	}

	function targetName(type: string, targetId: string): string {
		if (type === 'container') return targetId;
		const stack = stacks.data?.find((s) => s.id === targetId);
		return stack?.displayName || stack?.name || targetId;
	}

	function plural(n: number, one: string, many: string): string {
		return `${n} ${n === 1 ? one : many}`;
	}

	async function check() {
		checking = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/environment-update-policies/{policyId}/checks', {
					params: {
						path: { policyId: id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					}
				})
			);
			toast.success(`Checking ${plural(out.jobs.length, 'target', 'targets')} for updates`);
			await qc.invalidateQueries({ queryKey: ['policies'] });
		} catch (e) {
			error = e;
		} finally {
			checking = false;
		}
	}

	async function loadPreview() {
		previewing = true;
		error = null;
		try {
			preview = await unwrap(
				api.POST('/api/v1/environment-update-policies/{policyId}/previews', {
					params: { path: { policyId: id } }
				})
			);
		} catch (e) {
			error = e;
		} finally {
			previewing = false;
		}
	}

	async function apply() {
		if (!preview) return;
		applying = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/environment-update-policies/{policyId}/runs', {
					params: {
						path: { policyId: id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { fingerprint: preview.fingerprint }
				})
			);
			preview = null;
			toast.success(`Started ${plural(out.jobs.length, 'update', 'updates')}`);
			await qc.invalidateQueries({ queryKey: ['policies'] });
		} catch (e) {
			error = e;
		} finally {
			applying = false;
		}
	}

	async function remove() {
		const p = policy.data;
		if (!p) return;
		await unwrapEmpty(
			api.DELETE('/api/v1/environment-update-policies/{policyId}', {
				params: { path: { policyId: id }, header: { 'If-Match': ifMatch(p.revision) } }
			})
		);
		toast.success(`Deleted update policy ${p.name}`);
		await qc.invalidateQueries({ queryKey: ['policies'] });
		await goto(routes.updates());
	}

	const menu = $derived.by<MenuEntry[]>(() =>
		manage
			? [
					{ label: 'Edit policy', icon: Pencil, href: routes.updatePolicyEdit(id) },
					{ separator: true },
					{
						label: 'Delete policy',
						icon: Trash2,
						tone: 'danger',
						onSelect: () => (deleteOpen = true)
					}
				]
			: []
	);

	const previewRows = $derived<PreviewRow[]>(
		(preview?.targets ?? []).flatMap((t) =>
			t.items.map((item) => ({
				key: `${t.policyId}/${item.id}`,
				target: `${environmentName(envs.data, t.environmentId)} / ${targetName(t.type, t.id)}`,
				drift: t.sourceDrift,
				item
			}))
		)
	);
	const drifted = $derived((preview?.targets ?? []).filter((t) => t.sourceDrift));

	const targetColumns: Column<Target>[] = [
		{
			id: 'target',
			header: 'Target',
			cell: targetCell,
			sortValue: (t) => targetName(t.type, t.id),
			stack: 'title'
		},
		{
			id: 'environment',
			header: 'Environment',
			cell: envCell,
			sortValue: (t) => environmentName(envs.data, t.environmentId),
			width: '200px',
			stack: 'meta'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (t) =>
				t.inactive ? -1 : t.candidateSummary.available + t.candidateSummary.failed,
			width: '220px',
			stack: 'status'
		}
	];

	const previewColumns: Column<PreviewRow>[] = [
		{ id: 'target', header: 'Target', cell: previewTargetCell, stack: 'title' },
		{ id: 'service', header: 'Service', cell: serviceCell, width: '180px', stack: 'meta' },
		{
			id: 'status',
			header: 'Status',
			cell: previewStatusCell,
			width: '180px',
			stack: 'status'
		},
		{ id: 'digests', header: 'Digest', cell: digestsCell }
	];
</script>

{#snippet targetCell(t: Target)}
	<span class="target">
		{#if t.type === 'stack'}<Layers size={14} aria-hidden="true" />{:else}<PackageCheck
				size={14}
				aria-hidden="true"
			/>{/if}
		{#if t.type === 'stack'}<a href={routes.stack(t.id)}>{targetName(t.type, t.id)}</a
			>{:else}{targetName(t.type, t.id)}{/if}
	</span>
{/snippet}
{#snippet envCell(t: Target)}{environmentName(envs.data, t.environmentId)}{/snippet}
{#snippet statusCell(t: Target)}
	{@const s = t.candidateSummary}
	{#if t.inactive}
		<Badge tone="neutral">Excluded</Badge>
	{:else if s.failed || s.quarantined}
		<Badge tone="danger" dot>{plural(s.failed + s.quarantined, 'failure', 'failures')}</Badge>
	{:else if s.available}
		<Badge tone="warn" dot>{plural(s.available, 'update', 'updates')} available</Badge>
	{:else if !s.lastCheckAt}
		<Badge tone="neutral" dot>Not checked yet</Badge>
	{:else}
		<Badge tone="ok" dot>Up to date</Badge>
	{/if}
{/snippet}
{#snippet previewTargetCell(r: PreviewRow)}{r.target}{/snippet}
{#snippet serviceCell(r: PreviewRow)}{r.item.service}{/snippet}
{#snippet previewStatusCell(r: PreviewRow)}
	{@const st = candidateStatus(r.item.status)}
	<Badge tone={st.tone} dot>{st.label}</Badge>
	{#if reasonLabel(r.item)}<span class="muted reason">{reasonLabel(r.item)}</span>{/if}
{/snippet}
{#snippet digestsCell(r: PreviewRow)}
	{#if r.item.candidateDigest}
		<span class="pair">
			<Digest value={r.item.currentDigest} copy={false} />
			<span class="muted" aria-hidden="true">→</span>
			<span class="sr-only">to</span>
			<Digest value={r.item.candidateDigest} copy={false} />
		</span>
	{:else}<span class="muted">—</span>{/if}
{/snippet}

<Page>
	<QueryView
		query={policy}
		errorTitle="The update policy could not be loaded."
		notFoundTitle="This update policy does not exist."
		notFoundDescription="It was deleted, or you no longer have access to it."
	>
		{#snippet children(p: EnvironmentUpdatePolicy)}
			<PageHeader
				title={p.name}
				icon={PackageCheck}
				color="violet"
				description="Follows the digests behind the tags of every stack and DockYard-managed container in scope. Your Compose files and tags never change."
				meta={[{ icon: Server, label: scopeLabel(p) }]}
			>
				{#snippet actions()}
					<Button icon={RefreshCw} loading={checking} onclick={check}
						>Check for updates</Button
					>
					<Button variant="primary" loading={previewing} onclick={loadPreview}
						>Preview updates</Button
					>
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

			{#if error}
				<Notice tone="danger" title="The action did not start" live="alert"
					>{actionError(error)}</Notice
				>
			{/if}

			{#if preview}
				<Card
					title="Update preview"
					subtitle="What an update would change now. Applying pulls the new images and recreates the services that changed."
					padding="none"
				>
					{#snippet actions()}
						<Button variant="ghost" size="sm" onclick={() => (preview = null)}
							>Close preview</Button
						>
						<Button
							variant="primary"
							size="sm"
							loading={applying}
							disabled={!previewRows.some(
								(r) => r.item.eligible && r.item.status === 'update_available'
							)}
							onclick={apply}>Apply updates</Button
						>
					{/snippet}
					{#if drifted.length}
						<div class="inset">
							<Notice
								tone="warn"
								icon={TriangleAlert}
								title="Undeployed source changes"
								live="none"
							>
								Deploy the source changes of {drifted
									.map((t) => targetName(t.type, t.id))
									.join(', ')} before updating; they are skipped until then.
							</Notice>
						</div>
					{/if}
					<Table
						label="Update preview of {p.name}"
						rows={previewRows}
						columns={previewColumns}
						rowKey={(r) => r.key}
					>
						{#snippet empty()}<EmptyState
								icon={PackageCheck}
								color="violet"
								title="Nothing to update."
								description="Check for updates first, or every target already runs the registry's digest."
								level={3}
								compact
							/>{/snippet}
					</Table>
				</Card>
			{/if}

			<Card
				title="Targets"
				subtitle="Managed stacks and DockYard-managed standalone containers in scope, unless excluded."
				padding="none"
			>
				{#if targets.isPending}
					<div class="inset"><Skeleton lines={3} height="20px" /></div>
				{:else if targets.isError}
					<ErrorState
						error={targets.error}
						title="The targets could not be loaded."
						onretry={() => targets.refetch()}
						compact
					/>
				{:else}
					<Table
						label="Targets of {p.name}"
						rows={targets.data ?? []}
						columns={targetColumns}
						rowKey={(t) => t.policyId}
						sort={{ column: 'target', direction: 'asc' }}
					>
						{#snippet empty()}<EmptyState
								icon={PackageCheck}
								color="violet"
								title="Nothing to update in this scope."
								description="Stacks deployed by DockYard and standalone containers it created appear here."
								level={3}
								compact
							/>{/snippet}
					</Table>
				{/if}
			</Card>

			<Card title="Schedules">
				<Facts
					columns={2}
					items={[
						{ label: 'Checks', render: checkSched },
						{ label: 'Automatic updates', render: runSched },
						{ label: 'Update window', value: windowText(p.window) },
						{
							label: 'Health wait',
							value: p.waitTimeoutSeconds ? `${p.waitTimeoutSeconds} s` : 'Default'
						},
						{
							label: 'Excluded',
							value:
								[
									p.excludeStacks.length &&
										plural(p.excludeStacks.length, 'stack', 'stacks'),
									p.excludeContainers.length &&
										plural(
											p.excludeContainers.length,
											'container',
											'containers'
										)
								]
									.filter(Boolean)
									.join(', ') || 'Nothing'
						}
					]}
				/>
			</Card>

			{#snippet checkSched()}
				<ScheduleSummary
					cron={p.checkSchedule.cron ?? ''}
					timeZone={p.checkSchedule.timeZone ?? ''}
					enabled={p.checkSchedule.enabled}
				/>
			{/snippet}
			{#snippet runSched()}
				<ScheduleSummary
					cron={p.runSchedule.cron ?? ''}
					timeZone={p.runSchedule.timeZone ?? ''}
					enabled={p.runSchedule.enabled}
				/>
			{/snippet}

			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Delete update policy {p.name}"
				consequences={[
					'Removes the policy with its candidates, quarantine lists and update history.',
					'Nothing in its scope is checked or updated afterwards.',
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
	.target {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--text-strong);
	}

	.pair {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
	}

	.reason {
		display: block;
		margin-top: var(--space-1);
		font-size: var(--text-caption);
	}

	.inset {
		padding: var(--space-4);
	}
</style>
