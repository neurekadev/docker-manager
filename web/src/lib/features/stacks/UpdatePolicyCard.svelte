<script lang="ts">
	// The stack's update policy (#20): opt in, which services follow their
	// tag, the check and update schedules (both start off), the candidates
	// with their digests and eligibility reasons, quarantined digests and
	// the recent history. Changes save with If-Match; nothing is automatic
	// until a schedule is enabled.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { myPermissionsQuery } from '$lib/api/queries';
	import {
		Badge,
		Button,
		Card,
		Checkbox,
		ConfirmDialog,
		CronField,
		EmptyState,
		ErrorState,
		Skeleton,
		StatusBadge,
		Switch,
		Table,
		TextField,
		errorView,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';
	import {
		checkUpdates,
		createUpdatePolicy,
		deleteUpdatePolicy,
		patchUpdatePolicy
	} from './actions';
	import { canInEnvironment, candidateStatus, shortDigest, stackTitle } from './model';
	import {
		candidatesQuery,
		stackImageStatusQuery,
		stackKeys,
		updatePoliciesQuery,
		updatePolicyQuery,
		type Stack,
		type UpdateCandidate
	} from './queries';
	import type { JobTray } from './tray.svelte';

	interface Props {
		stack: Stack;
		tray: JobTray;
	}

	let { stack, tray }: Props = $props();
	const queryClient = useQueryClient();
	const title = $derived(stackTitle(stack));

	const perms = createQuery(() => myPermissionsQuery());
	const policies = createQuery(() => updatePoliciesQuery(stack.environmentId));
	const found = $derived(
		policies.data?.find((p) => p.target.type === 'stack' && p.target.id === stack.id)
	);
	const policy = createQuery(() => ({ ...updatePolicyQuery(found?.id ?? ''), enabled: !!found }));
	const p = $derived(policy.data);
	const candidates = createQuery(() => ({
		...candidatesQuery(found?.id ?? ''),
		enabled: !!found && p?.view === 'full'
	}));
	// Eligibility per service is known before any check (#20 image status).
	const images = createQuery(() => stackImageStatusQuery(stack.id));
	function eligibility(service: string): string | undefined {
		const c = candidates.data?.find((x) => x.service === service);
		const i = images.data?.find((x) => x.service === service);
		if (c && !c.eligible) return c.reasonMessage ?? 'Not eligible';
		if (!c && i && !i.eligible) return i.reasonMessage ?? 'Not eligible';
		if (c?.nonVersionTag || i?.nonVersionTag)
			return 'Not a version tag: what it points to can change meaning.';
		return undefined;
	}
	const canManage = $derived(
		canInEnvironment(perms.data, 'update_policy.manage', stack.environmentId) ||
			!!p?.actions.includes('update_policy.manage')
	);
	const canCheck = $derived(stack.actions.includes('update.check'));

	// Form state, reset whenever a new revision of the policy arrives.
	let name = $state('');
	let optIn = $state<string[]>([]);
	let checkOn = $state(false);
	let checkCron = $state('0 4 * * *');
	let checkZone = $state('UTC');
	let runOn = $state(false);
	let runCron = $state('30 4 * * *');
	let runZone = $state('UTC');
	let loadedRevision = -1;
	const services = $derived((stack.services ?? []).map((s) => s.name));
	$effect(() => {
		const cur = p;
		if (!cur || cur.revision === undefined || cur.revision === loadedRevision) return;
		untrack(() => {
			loadedRevision = cur.revision!;
			name = cur.name;
			const ex = new Set(cur.excludeServices ?? []);
			optIn = (cur.services?.length ? cur.services : services).filter((s) => !ex.has(s));
			checkOn = cur.checkSchedule?.enabled ?? false;
			checkCron = cur.checkSchedule?.cron ?? checkCron;
			checkZone = cur.checkSchedule?.timeZone ?? checkZone;
			runOn = cur.runSchedule?.enabled ?? false;
			runCron = cur.runSchedule?.cron ?? runCron;
			runZone = cur.runSchedule?.timeZone ?? runZone;
		});
	});

	let saving = $state(false);
	let error = $state<string | null>(null);
	let creating = $state(false);
	let deleting = $state(false);

	async function optInNow() {
		creating = true;
		try {
			await createUpdatePolicy({
				name: `${title} updates`,
				environmentId: stack.environmentId,
				target: { type: 'stack', id: stack.id }
			});
			toast.success(`Opted ${title} in to updates`, {
				body: 'Checks and updates run only when you start them until you enable a schedule.'
			});
			await queryClient.invalidateQueries({
				queryKey: stackKeys.updatePolicies(stack.environmentId)
			});
		} catch (e) {
			toast.error(`${title} was not opted in to updates`, { body: errorView(e).message });
		} finally {
			creating = false;
		}
	}

	async function save() {
		if (!p) return;
		saving = true;
		error = null;
		try {
			const all = optIn.length === services.length;
			const next = await patchUpdatePolicy(p, {
				name: name.trim() || p.name,
				services: all ? [] : optIn,
				excludeServices: all ? [] : services.filter((s) => !optIn.includes(s)),
				checkSchedule: { enabled: checkOn, cron: checkCron.trim(), timeZone: checkZone },
				runSchedule: { enabled: runOn, cron: runCron.trim(), timeZone: runZone }
			});
			queryClient.setQueryData(stackKeys.updatePolicy(p.id), next);
			void queryClient.invalidateQueries({ queryKey: ['policies'] });
			toast.success(`Saved the update policy of ${title}`);
		} catch (e) {
			const v = errorView(e);
			error =
				v.status === 412
					? 'The policy changed meanwhile. The current settings are loaded; check them and save again.'
					: v.message;
			if (v.status === 412) {
				loadedRevision = -1;
				void policy.refetch();
			}
		} finally {
			saving = false;
		}
	}

	async function checkNow() {
		if (!p) return;
		try {
			const job = await checkUpdates(p.id);
			tray.add(job, {
				title: `Check ${title} for updates`,
				success: `Checked ${title} for updates`,
				failure: `${title} was not checked for updates`,
				onfinish: () => {
					void queryClient.invalidateQueries({ queryKey: stackKeys.updatePolicy(p.id) });
					void queryClient.invalidateQueries({
						queryKey: stackKeys.imageStatus(stack.id)
					});
				}
			});
		} catch (e) {
			toast.error(`${title} was not checked for updates`, { body: errorView(e).message });
		}
	}

	async function remove() {
		if (!p) return;
		await deleteUpdatePolicy(p);
		toast.success(`Removed the update policy of ${title}`);
		await queryClient.invalidateQueries({ queryKey: ['policies'] });
	}

	const TONE: Record<string, string> = {
		update_available: 'update_available',
		up_to_date: 'succeeded',
		quarantined: 'failed',
		check_failed: 'failed',
		run_failed: 'failed',
		ineligible: 'skipped',
		unchecked: 'queued'
	};

	const columns: Column<UpdateCandidate>[] = [
		{
			id: 'service',
			header: 'Service',
			cell: svcCell,
			sortValue: (c) => c.service,
			stack: 'title',
			width: '160px'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (c) => c.status,
			stack: 'status',
			width: '170px'
		},
		{ id: 'ref', header: 'Image', cell: refCell, mono: true },
		{ id: 'digests', header: 'Current → candidate', cell: digestCell, mono: true },
		{ id: 'note', header: 'Details', cell: noteCell }
	];
</script>

{#snippet svcCell(c: UpdateCandidate)}<span class="svc mono">{c.service}</span>{/snippet}
{#snippet statusCell(c: UpdateCandidate)}
	<StatusBadge status={TONE[c.status] ?? c.status} label={candidateStatus(c.status)} />
{/snippet}
{#snippet refCell(c: UpdateCandidate)}
	<span class="ref" title={c.platform ? `${c.reference} (${c.platform})` : c.reference}
		>{c.reference}</span
	>
{/snippet}
{#snippet digestCell(c: UpdateCandidate)}
	<span title="{c.currentDigest ?? ''} → {c.candidateDigest ?? ''}">
		{c.currentDigest ? shortDigest(c.currentDigest) : '—'}
		{#if c.candidateDigest && c.candidateDigest !== c.currentDigest}→ {shortDigest(
				c.candidateDigest
			)}{/if}
	</span>
{/snippet}
{#snippet noteCell(c: UpdateCandidate)}
	<span class="note">
		{#if !c.eligible}{c.reasonMessage ?? c.reason?.replaceAll('_', ' ')}
		{:else if c.errorMessage}{c.errorMessage}
		{:else if c.nonVersionTag}<span class="warn"
				>{c.reasonMessage ?? 'Not a version tag: it can change meaning.'}</span
			>
		{:else if c.checkedAt}Checked {formatRelative(c.checkedAt)}{:else}—{/if}
		{#if c.guidance}<span class="muted"> {c.guidance}</span>{/if}
	</span>
{/snippet}

<Card title="Updates" id="updates" subtitle="Follow each tag's newest digest; files never change">
	{#snippet actions()}
		{#if p && canCheck}
			<Button size="sm" icon={RefreshCw} onclick={checkNow}>Check now</Button>
		{/if}
	{/snippet}
	{#if policies.isPending || (found && policy.isPending)}
		<div aria-busy="true"><Skeleton lines={4} /></div>
	{:else if policies.isError || policy.isError}
		<ErrorState
			error={policies.error ?? policy.error}
			title="The update policy could not be loaded."
			onretry={() => (policies.refetch(), policy.refetch())}
			compact
		/>
	{:else if !found}
		<EmptyState
			icon={PackageCheck}
			color="green"
			title="{title} does not follow image updates."
			description={canManage
				? 'Opt in to check its tags for newer digests. Checks and updates stay manual until you enable a schedule.'
				: 'Ask someone who manages update policies on this environment to opt it in.'}
			level={3}
			compact
		>
			{#snippet actions()}
				{#if canManage}<Button variant="primary" loading={creating} onclick={optInNow}
						>Opt in to updates</Button
					>{/if}
			{/snippet}
		</EmptyState>
	{:else if p}
		<div class="summary">
			{#if p.summary}
				{#if p.summary.available}<Badge tone="warn" dot
						>{p.summary.available} update{p.summary.available === 1 ? '' : 's'} available</Badge
					>{/if}
				{#if p.summary.upToDate}<Badge tone="ok" dot>{p.summary.upToDate} up to date</Badge
					>{/if}
				{#if p.summary.quarantined}<Badge tone="danger" dot
						>{p.summary.quarantined} quarantined</Badge
					>{/if}
				{#if p.summary.failed}<Badge tone="danger" dot>{p.summary.failed} failed</Badge
					>{/if}
				{#if p.summary.ineligible}<Badge>{p.summary.ineligible} not eligible</Badge>{/if}
				{#if p.summary.unchecked}<Badge>{p.summary.unchecked} not checked yet</Badge>{/if}
				<span class="muted">
					{p.summary.lastCheckAt
						? `Last checked ${formatRelative(p.summary.lastCheckAt)}`
						: 'Never checked'}
				</span>
			{/if}
		</div>

		{#if p.view === 'full'}
			<div class="candidates">
				{#if candidates.isPending}
					<Skeleton lines={3} />
				{:else if candidates.data}
					<Table
						label="Update candidates of {title}"
						rows={candidates.data}
						{columns}
						rowKey={(c) => c.id}
					>
						{#snippet empty()}<p class="pad muted">
								Not checked yet. Check now to compare each opted-in tag with its
								registry.
							</p>{/snippet}
					</Table>
				{/if}
			</div>

			{#if p.quarantine?.length}
				<section class="block" aria-labelledby="quarantine-title">
					<h3 id="quarantine-title">Quarantined digests</h3>
					<p class="muted">
						An update to these failed, so they are never applied automatically. To go
						back, pin an older digest (image@sha256:…) in your own Compose file and
						deploy.
					</p>
					<ul class="plain" role="list">
						{#each p.quarantine as q (`${q.service}/${q.digest}`)}
							<li>
								<span class="mono">{q.service}</span>
								<span class="mono muted" title={q.digest}
									>{shortDigest(q.digest)}</span
								>
								{#if q.errorClass}<span class="muted"
										>({q.errorClass.replaceAll('_', ' ')})</span
									>{/if}
								<span class="muted">{formatRelative(q.createdAt)}</span>
							</li>
						{/each}
					</ul>
				</section>
			{/if}

			{#if canManage}
				<form
					class="form"
					onsubmit={(e) => {
						e.preventDefault();
						void save();
					}}
				>
					<div class="row">
						<TextField label="Policy name" bind:value={name} />
					</div>
					<fieldset class="services">
						<legend>Services that follow their tag</legend>
						{#each services as s (s)}
							<Checkbox
								label={s}
								checked={optIn.includes(s)}
								description={eligibility(s)}
								onchange={(e) => {
									const on = (e.currentTarget as HTMLInputElement).checked;
									optIn = on ? [...optIn, s] : optIn.filter((x) => x !== s);
								}}
							/>
						{/each}
					</fieldset>
					<div class="schedules">
						<fieldset class="schedule">
							<legend>Automatic checks</legend>
							<Switch
								bind:checked={checkOn}
								label="Check on a schedule"
								description="Compares digests with the registry; nothing is pulled."
							/>
							{#if checkOn}
								<CronField
									label="Check schedule"
									bind:cron={checkCron}
									bind:timeZone={checkZone}
									kind="update_check"
								/>
							{/if}
						</fieldset>
						<fieldset class="schedule">
							<legend>Automatic updates</legend>
							<Switch
								bind:checked={runOn}
								label="Update on a schedule"
								description="Pulls and recreates what changed. There is no automatic rollback."
							/>
							{#if runOn}
								<CronField
									label="Update schedule"
									bind:cron={runCron}
									bind:timeZone={runZone}
									kind="update_run"
								/>
							{/if}
						</fieldset>
					</div>
					{#if p.checkSchedule?.enabled && p.checkSchedule.nextRun}
						<p class="muted">
							Next check {formatDateTime(
								p.checkSchedule.nextRun.at,
								p.checkSchedule.timeZone
							)}.
						</p>
					{/if}
					{#if p.runSchedule?.enabled && p.runSchedule.nextRun}
						<p class="muted">
							Next update {formatDateTime(
								p.runSchedule.nextRun.at,
								p.runSchedule.timeZone
							)}.
						</p>
					{/if}
					{#if error}<p class="error" role="alert">{error}</p>{/if}
					<div class="buttons">
						<Button
							variant="danger-soft"
							icon={Trash2}
							onclick={() => (deleting = true)}>Remove policy</Button
						>
						<Button variant="primary" type="submit" loading={saving}>Save policy</Button
						>
					</div>
				</form>
			{/if}

			{#if p.recentHistory?.length}
				<section class="block" aria-labelledby="history-title">
					<h3 id="history-title">Recent updates</h3>
					<ul class="plain" role="list">
						{#each p.recentHistory as h (`${h.at}/${h.service}`)}
							<li>
								<span class="mono">{h.service}</span>
								{h.outcome.replaceAll('_', ' ')}
								{#if h.fromDigest && h.toDigest}<span class="mono muted"
										>{shortDigest(h.fromDigest)} → {shortDigest(
											h.toDigest
										)}</span
									>{/if}
								<span class="muted">{formatRelative(h.at)}</span>
							</li>
						{/each}
					</ul>
				</section>
			{/if}
		{/if}
	{/if}
</Card>

{#if p}
	<ConfirmDialog
		bind:open={deleting}
		title="Remove the update policy of {title}?"
		consequences={[
			'Removes the policy with its candidates, quarantined digests and update history.',
			'Running containers and your Compose files are not touched; the audit log and job history stay.'
		]}
		confirmLabel="Remove policy"
		tone="danger"
		onconfirm={remove}
	/>
{/if}

<style>
	.summary {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin-bottom: var(--space-4);
	}

	.candidates {
		margin: 0 calc(-1 * var(--space-5));
		border-top: 1px solid var(--border-subtle);
		border-bottom: 1px solid var(--border-subtle);
	}

	.pad {
		padding: var(--space-4) var(--space-5);
	}

	.svc {
		color: var(--text-strong);
	}

	.ref {
		display: block;
		max-width: 260px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.note {
		color: var(--text-default);
	}

	.warn {
		color: var(--warn);
	}

	.block {
		display: grid;
		gap: var(--space-2);
		margin-top: var(--space-5);
	}

	h3 {
		font-size: var(--text-control);
		font-weight: var(--weight-semibold);
	}

	.plain {
		display: grid;
		gap: 4px;
		margin: 0;
		padding-left: 18px;
	}

	.plain li {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.form {
		display: grid;
		gap: var(--space-5);
		margin-top: var(--space-5);
	}

	.row {
		max-width: 420px;
	}

	fieldset {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		border: 0;
	}

	legend {
		margin-bottom: var(--space-2);
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.services {
		grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
	}

	.services legend {
		grid-column: 1 / -1;
	}

	.schedules {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
		gap: var(--space-5);
	}

	.schedule {
		align-content: start;
	}

	.buttons {
		display: flex;
		justify-content: space-between;
		gap: var(--space-2);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
