<script lang="ts">
	// Update preview (#22 "Update" button, #20): opening it runs a digest
	// check of the stack's update policy (nothing is pulled), then shows the
	// preview: current and candidate digests per service, services restarted
	// with them, other stacks sharing the tag, expected downtime and source
	// drift. "Update now" runs exactly that preview (its fingerprint). A
	// stack without a policy can be opted in here (schedules stay off).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import type { Job, Schema } from '$lib/api/client';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Drawer,
		EmptyState,
		ErrorState,
		JobProgress,
		Notice,
		Skeleton,
		errorMessage,
		toast
	} from '$lib/ui';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import { checkUpdates, createUpdatePolicy, previewUpdate, runUpdate } from './actions';
	import { canInEnvironment, candidateStatus, shortDigest, stackTitle } from './model';
	import { stackKeys, updatePoliciesQuery, type Stack } from './queries';
	import type { JobTray } from './tray.svelte';

	interface Props {
		open: boolean;
		stack: Stack;
		tray: JobTray;
	}

	let { open = $bindable(false), stack, tray }: Props = $props();
	const queryClient = useQueryClient();
	const title = $derived(stackTitle(stack));

	const perms = createQuery(() => myPermissionsQuery());
	const policies = createQuery(() => ({
		...updatePoliciesQuery(stack.environmentId),
		enabled: open
	}));
	const policy = $derived(
		policies.data?.find((p) => p.target.type === 'stack' && p.target.id === stack.id)
	);
	const canManage = $derived(
		canInEnvironment(perms.data, 'update_policy.manage', stack.environmentId)
	);
	const canRun = $derived(stack.actions.includes('update.run'));

	type Phase = 'idle' | 'checking' | 'previewing' | 'preview' | 'failed';
	let phase = $state<Phase>('idle');
	let checkJob = $state<string | null>(null);
	let preview = $state<Schema<'UpdatePreview'> | null>(null);
	let failure = $state<unknown>(null);
	let busy = $state(false);

	// A check starts each time the drawer opens on a stack with a policy.
	$effect(() => {
		if (!open) {
			untrack(() => {
				phase = 'idle';
				checkJob = null;
				preview = null;
				failure = null;
			});
			return;
		}
		const p = policy;
		if (p && untrack(() => phase) === 'idle') untrack(() => void check(p.id));
	});

	async function check(policyId: string) {
		phase = 'checking';
		failure = null;
		preview = null;
		try {
			const job = await checkUpdates(policyId);
			checkJob = job.id;
		} catch (e) {
			failure = e;
			phase = 'failed';
		}
	}

	async function checked(policyId: string, job: Job) {
		void queryClient.invalidateQueries({ queryKey: stackKeys.imageStatus(stack.id) });
		void queryClient.invalidateQueries({ queryKey: stackKeys.updatePolicy(policyId) });
		if (job.state !== 'succeeded' && job.state !== 'partial') {
			failure = new Error(job.error?.message ?? 'The digest check did not finish.');
			phase = 'failed';
			return;
		}
		phase = 'previewing';
		try {
			preview = await previewUpdate(policyId);
			phase = 'preview';
		} catch (e) {
			failure = e;
			phase = 'failed';
		}
	}

	async function optIn() {
		busy = true;
		try {
			const p = await createUpdatePolicy({
				name: `${title} updates`,
				environmentId: stack.environmentId,
				target: { type: 'stack', id: stack.id }
			});
			toast.success(`Opted ${title} in to updates`, {
				body: 'Automatic checks and updates stay off until you enable them under Policies.'
			});
			await queryClient.invalidateQueries({
				queryKey: stackKeys.updatePolicies(stack.environmentId)
			});
			await check(p.id);
		} catch (e) {
			toast.error(`${title} was not opted in to updates`, { body: errorMessage(e) });
		} finally {
			busy = false;
		}
	}

	async function run() {
		if (!policy || !preview) return;
		busy = true;
		try {
			const job = await runUpdate(policy.id, preview.fingerprint);
			tray.add(job, {
				title: `Update ${title}`,
				success: `Updated ${title}`,
				failure: `${title} was not updated`
			});
			open = false;
		} catch (e) {
			const code = (e as { apiError?: { code?: string } }).apiError?.code;
			if (code === 'update_preview_stale') {
				toast.warn('The preview changed', {
					body: 'Something changed since the check. Check again.'
				});
				await check(policy.id);
			} else {
				toast.error(`${title} was not updated`, { body: errorMessage(e) });
			}
		} finally {
			busy = false;
		}
	}

	const items = $derived(preview?.items ?? []);
</script>

<Drawer bind:open title="Update {title}" size="560px">
	<div class="body">
		<p class="intro">
			Updates follow each service's tag to its newest digest for this host's platform. The
			Compose files and the tag text never change, and there is no automatic rollback.
		</p>

		{#if policies.isPending}
			<div aria-busy="true"><Skeleton lines={4} /></div>
		{:else if policies.isError}
			<ErrorState
				error={policies.error}
				title="The update policies could not be loaded."
				onretry={() => policies.refetch()}
				compact
			/>
		{:else if !policy}
			<EmptyState
				icon={PackageCheck}
				color="green"
				title="{title} is not opted in to updates."
				description={canManage
					? 'Opt in to check its images now. Scheduled checks and updates stay off until you enable them under Policies.'
					: 'Ask someone who manages update policies on this environment to opt it in.'}
				level={3}
				compact
			>
				{#snippet actions()}
					{#if canManage}
						<Button variant="primary" loading={busy} onclick={optIn}
							>Opt in and check</Button
						>
					{/if}
				{/snippet}
			</EmptyState>
		{:else}
			{#if phase === 'checking' && checkJob}
				{#key checkJob}
					<JobProgress
						jobId={checkJob}
						title="Check {title} for updates"
						variant="inline"
						notices={null}
						onfinish={(j) => checked(policy.id, j)}
					/>
				{/key}
			{:else if phase === 'checking' || phase === 'previewing'}
				<div aria-busy="true"><Skeleton lines={3} /></div>
			{:else if phase === 'failed'}
				<ErrorState
					error={failure}
					title="The update check did not finish."
					compact
					onretry={() => check(policy.id)}
				/>
			{:else if phase === 'preview' && preview}
				{#if preview.sourceDrift}
					<Notice tone="warn" title="{title} has undeployed changes.">
						The files on disk differ from the deployed revision, so updates are refused.
						Deploy the stack (or restore the deployed revision) first.
						{#snippet actions()}
							<Button size="sm" href={routes.stack(stack.id, 'revisions')}
								>Open revisions</Button
							>
						{/snippet}
					</Notice>
				{/if}
				{#if items.length === 0}
					<EmptyState
						icon={PackageCheck}
						color="green"
						title="Everything is up to date."
						description="No service's tag points to a newer digest for this host."
						level={3}
						compact
					/>
				{:else}
					<section class="section" aria-labelledby="upd-items">
						<h3 id="upd-items">Services to update</h3>
						<ul class="items" role="list">
							{#each items as it (it.candidate.id)}
								<li class="item">
									<div class="row">
										<span class="svc mono">{it.candidate.service}</span>
										{#if it.running}<Badge tone="ok" dot>Running</Badge
											>{:else}<Badge>Stopped, stays stopped</Badge>{/if}
									</div>
									<div class="ref mono">{it.candidate.reference}</div>
									<dl class="digests">
										<dt>Current</dt>
										<dd class="mono" title={it.candidate.currentDigest}>
											{it.candidate.currentDigest
												? shortDigest(it.candidate.currentDigest)
												: '—'}
										</dd>
										<dt>Candidate</dt>
										<dd class="mono" title={it.candidate.candidateDigest}>
											{it.candidate.candidateDigest
												? shortDigest(it.candidate.candidateDigest)
												: '—'}
										</dd>
										<dt>Downtime</dt>
										<dd>{it.downtime}</dd>
									</dl>
									{#if it.candidate.nonVersionTag}
										<p class="warn">
											{it.candidate.reasonMessage ??
												'This tag is not a version: what it points to can change meaning.'}
										</p>
									{/if}
								</li>
							{/each}
						</ul>
					</section>
				{/if}
				{#if preview.restarted.length}
					<section class="section">
						<h3>Restarted with them</h3>
						<p class="muted">
							{preview.restarted.join(', ')} (depends_on with restart: true).
						</p>
					</section>
				{/if}
				{#if preview.sharedTag.length}
					<section class="section">
						<h3>Other consumers of these tags</h3>
						<p class="muted">
							Pulling moves the tag on this environment for them too. They keep their
							current container until they are next recreated.
						</p>
						<ul class="plain" role="list">
							{#each preview.sharedTag as c (`${c.stackId ?? ''}/${c.service ?? c.container ?? ''}/${c.reference}`)}
								<li>
									<span class="mono">{c.reference}</span>:
									{c.stackName ? `${c.stackName} · ${c.service}` : c.container}
								</li>
							{/each}
						</ul>
					</section>
				{/if}
				{#if preview.skipped.length}
					<section class="section">
						<h3>Not updated</h3>
						<ul class="plain" role="list">
							{#each preview.skipped as c (c.id)}
								<li>
									<span class="mono">{c.service}</span>:
									{candidateStatus(
										c.status
									)}{#if !c.eligible || c.errorMessage}<span class="muted">
											· {c.errorMessage ?? c.reasonMessage}</span
										>{/if}
									{#if c.guidance}<span class="muted"> {c.guidance}</span>{/if}
								</li>
							{/each}
						</ul>
					</section>
				{/if}
				{#each preview.notes as n (n)}<p class="muted">{n}</p>{/each}
				{#if !preview.inWindow}
					<p class="muted">
						Now is outside the policy's update window; manual updates run anyway.
					</p>
				{/if}
			{/if}
		{/if}
	</div>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)}>Close</Button>
		{#if policy && (phase === 'preview' || phase === 'failed')}
			<Button icon={RefreshCw} onclick={() => check(policy.id)}>Check again</Button>
		{/if}
		{#if policy && canRun && phase === 'preview' && items.length && !preview?.sourceDrift}
			<Button variant="primary" loading={busy} onclick={run}>Update now</Button>
		{/if}
	{/snippet}
</Drawer>

<style>
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		padding: var(--space-4) var(--space-5);
	}

	.intro {
		color: var(--text-muted);
	}

	.section {
		display: grid;
		gap: var(--space-2);
	}

	h3 {
		font-size: var(--text-control);
		font-weight: var(--weight-semibold);
		color: var(--text-strong);
	}

	.items {
		display: grid;
		gap: var(--space-3);
	}

	.item {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}

	.svc {
		color: var(--text-strong);
	}

	.ref {
		color: var(--text-muted);
		overflow-wrap: anywhere;
	}

	.digests {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 2px var(--space-3);
		margin: 0;
	}

	dt {
		color: var(--text-muted);
	}

	dd {
		margin: 0;
		color: var(--text-default);
		overflow-wrap: anywhere;
	}

	.warn {
		color: var(--warn);
		font-size: var(--text-caption);
	}

	.plain {
		display: grid;
		gap: 4px;
		margin: 0;
		padding-left: 18px;
	}
</style>
