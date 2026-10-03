<script lang="ts">
	// Update preview and run (#20) of one target (a stack or a standalone
	// container). The preview is computed by the manager from the latest
	// check: what gets recreated (the image and tag; the current and new
	// digests behind "Digests", when the newer image was published if the
	// registry says so), expected downtime, dependents that restart,
	// other consumers of the same tag on the environment (the pull moves it
	// for them too), skipped candidates and source drift. Applying sends the preview's
	// fingerprint, so anything that changed since is refused, not guessed.
	// The update job shows in place of the preview: the one just started,
	// or a running update.run of the policy from the running jobs list (the
	// dialog opened again after a reload or elsewhere; docs/internal/web.md,
	// "Job progress after reload").
	import { untrack } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap, type Job } from '$lib/api/client';
	import {
		Badge,
		Button,
		Dialog,
		JobProgress,
		Notice,
		Skeleton,
		errorMessage,
		formatDateTime,
		toast
	} from '$lib/ui';
	import Digest from '$lib/features/common/Digest.svelte';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { policyLabel } from '$lib/shell/notices.svelte';
	import {
		imageLabel,
		publishedText,
		reasonLabel,
		type UpdatePolicy,
		type UpdatePreview
	} from './model';
	import { updateKeys } from './queries';

	let {
		open = $bindable(false),
		policy,
		canRun,
		name: given,
		onstart
	}: {
		open?: boolean;
		policy: UpdatePolicy;
		canRun: boolean;
		/** The target's name ("zerobyte"); default: the policy as users know it. */
		name?: string;
		/** The update job started, with its title (the page shows it after the dialog closes). */
		onstart?: (job: Job, title: string) => void;
	} = $props();

	const qc = useQueryClient();
	const name = $derived(given ?? policyLabel(policy));
	let preview = $state<UpdatePreview | null>(null);
	let loadError = $state<unknown>(null);
	let loading = $state(false);
	let runError = $state<unknown>(null);
	let starting = $state(false);
	// The policy's update jobs while the dialog is open (closing starts over).
	const runs = useTrackedJobs(() =>
		open ? { kinds: ['update.run'], policyId: policy.id } : null
	);
	const job = $derived(runs.entries[0]);

	async function load() {
		loading = true;
		loadError = null;
		preview = null;
		try {
			preview = await unwrap(
				api.POST('/api/v1/update-policies/{policyId}/previews', {
					params: { path: { policyId: policy.id } },
					body: {}
				})
			);
		} catch (e) {
			loadError = e;
		} finally {
			loading = false;
		}
	}

	// Computed once per opening (a refetched policy does not reset it).
	$effect(() => {
		if (open) untrack(() => (job ? undefined : void load()));
		if (!open) runError = null;
	});

	const count = $derived(preview?.items.length ?? 0);

	async function apply() {
		if (!preview) return;
		starting = true;
		runError = null;
		try {
			const started = await unwrap(
				api.POST('/api/v1/update-policies/{policyId}/runs', {
					params: {
						path: { policyId: policy.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: {
						candidates: preview.items.map((i) => i.candidate.id),
						previewFingerprint: preview.fingerprint
					}
				})
			);
			const title = `Update ${name}`;
			runs.add(started, title);
			onstart?.(started, title);
		} catch (e) {
			runError = e;
		} finally {
			starting = false;
		}
	}

	function finished(j: Job) {
		runs.markFinished(j);
		void qc.invalidateQueries({ queryKey: updateKeys.detail(policy.id) });
		void qc.invalidateQueries({ queryKey: ['policies', 'list'] });
		if (j.state === 'succeeded') toast.success(`Updated ${name}`);
		else if (j.state === 'partial')
			toast.warn(`Updated ${name} partly`, { body: j.error?.recovery });
		else
			toast.error(`${name} was not updated`, {
				body: j.error?.recovery ?? j.error?.message
			});
	}
</script>

<Dialog bind:open title="Update Preview" size="lg">
	{#if job}
		{#key job.id}
			<JobProgress
				jobId={job.id}
				title={job.title ?? `Update ${name}`}
				variant="panel"
				onfinish={finished}
			/>
		{/key}
	{:else if loading}
		<div aria-busy="true"><Skeleton lines={5} height="20px" /></div>
	{:else if loadError}
		<Notice tone="danger" title="The preview could not be computed" live="alert">
			{errorMessage(loadError)}
		</Notice>
	{:else if preview}
		<div class="preview">
			{#if preview.sourceDrift}
				<Notice tone="danger" title="Undeployed Changes Block Updates" live="none">
					The Compose files on disk differ from the deployed revision. Deploy the stack
					first, then check again.
				</Notice>
			{/if}
			{#if !preview.inWindow && policy.window}
				<Notice tone="info" title="Outside the Update Window" live="none">
					This manual update runs now.
				</Notice>
			{/if}

			{#if count === 0}
				<Notice tone="info" title="Nothing to Update" live="none">
					No service has a newer image. Run a check to look again.
				</Notice>
			{:else}
				<section>
					<h3>Recreated ({count})</h3>
					<ul class="items" role="list">
						{#each preview.items as item (item.candidate.id)}
							{@const published = publishedText(item.candidate)}
							<li>
								<div class="svc">
									<strong>{item.candidate.service}</strong>
									<span
										class="mono muted"
										title="{item.candidate.currentDigest ?? 'Unknown'} → {item
											.candidate.candidateDigest ?? 'Unknown'}"
										>{imageLabel(item.candidate)}</span
									>
									<span class="muted"
										>newer image{#if published},
											<span
												title="Published {formatDateTime(
													item.candidate.publishedAt
												)}">{published}</span
											>{/if}</span
									>
								</div>
								<div class="meta">
									{#if item.running}<Badge tone="ok" dot>Running</Badge
										>{:else}<Badge tone="neutral" dot
											>Stopped: Stays Stopped</Badge
										>{/if}
									{#if item.candidate.nonVersionTag}<Badge tone="warn"
											>Tag Can Change Meaning</Badge
										>{/if}
									<span class="muted">Downtime: {item.downtime}</span>
								</div>
								<Disclosure summary="Digests">
									<div class="digests">
										<span class="muted">Running</span>
										<Digest value={item.candidate.currentDigest} />
										<span class="muted">New</span>
										<Digest
											value={item.candidate.candidateDigest}
											tone="accent"
										/>
									</div>
								</Disclosure>
							</li>
						{/each}
					</ul>
				</section>
			{/if}

			{#if preview.restarted.length}
				<section>
					<h3>Restarted with Them</h3>
					<p class="muted">
						These running services depend on an updated one with <span class="mono"
							>restart: true</span
						>:
						{preview.restarted.join(', ')}.
					</p>
				</section>
			{/if}

			{#if preview.sharedTag.length}
				<Notice tone="warn" title="The Pull Moves Shared Tags" live="none">
					<p>
						Other containers use the same tags. They keep their current image until they
						are next recreated:
					</p>
					<ul class="plain" role="list">
						{#each preview.sharedTag as s (`${s.stackId ?? ''}${s.container ?? ''}${s.service ?? ''}${s.reference}`)}
							<li>
								{s.stackName ?? s.container ?? 'Container'}{s.service
									? ` / ${s.service}`
									: ''}
								<span class="mono muted">{s.reference}</span>
							</li>
						{/each}
					</ul>
				</Notice>
			{/if}

			{#if preview.skipped.length}
				<section>
					<h3>Left Out</h3>
					<ul class="plain" role="list">
						{#each preview.skipped as c (c.id)}
							<li>
								<strong>{c.service}</strong>
								<span class="muted"
									>— {reasonLabel(c) ??
										c.reasonMessage ??
										c.status.replaceAll('_', ' ')}</span
								>
							</li>
						{/each}
					</ul>
				</section>
			{/if}

			{#each preview.notes as note (note)}<p class="muted">{note}</p>{/each}

			{#if runError}
				<Notice tone="danger" title="The update did not start" live="alert">
					{actionError(runError, {
						update_preview_stale:
							'Something changed since this preview (a check, a deploy or another update). Review the new preview.',
						update_source_drift:
							'The stack has undeployed changes. Deploy it first, then update.',
						no_update_candidates: 'Nothing is left to update.'
					})}
				</Notice>
			{/if}
		</div>
	{/if}

	{#snippet footer()}
		{#if job}
			<Button variant="secondary" onclick={() => (open = false)}>Close</Button>
		{:else}
			<Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
			{#if runError}
				<Button variant="secondary" onclick={load}>Refresh Preview</Button>
			{/if}
			{#if canRun && preview && count > 0 && !preview.sourceDrift}
				<Button variant="primary" loading={starting} onclick={apply}>
					Update {count}
					{count === 1 ? 'Service' : 'Services'}
				</Button>
			{/if}
		{/if}
	{/snippet}
</Dialog>

<style>
	.preview {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	h3 {
		margin-bottom: var(--space-2);
		font-size: var(--text-control);
		font-weight: var(--weight-semibold);
		color: var(--text-strong);
	}

	.items {
		display: grid;
		gap: var(--space-2);
	}

	.items li {
		display: grid;
		gap: var(--space-1);
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	.svc {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
		align-items: baseline;
	}

	.svc strong {
		color: var(--text-strong);
	}

	.digests,
	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-caption);
	}

	.plain {
		display: grid;
		gap: 2px;
		margin-top: var(--space-1);
	}
</style>
