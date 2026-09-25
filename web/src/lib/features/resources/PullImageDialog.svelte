<script lang="ts">
	// Pull an image (#6, #19): reference, optional platform, the registry
	// connection it will use (match preview), then the pull job's progress
	// (layers) in place. Registry failures (401/403/429, not found,
	// unavailable) are explained with what to do next.
	import { untrack } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import Download from '@lucide/svelte/icons/download';
	import { api, unwrap, type Job } from '$lib/api/client';
	import type { EnvTarget } from '$lib/api/multi-env';
	import { queryKeys } from '$lib/api/queries';
	import {
		Button,
		Dialog,
		JobProgress,
		Notice,
		Select,
		TextField,
		fieldError,
		toast
	} from '$lib/ui';
	import RegistryMatchPreview from './RegistryMatchPreview.svelte';
	import { idempotencyKey } from './jobs.svelte';
	import { doneTitle, refusal, registryGuidance, type Refusal } from './refusals';

	interface Props {
		open?: boolean;
		/** Environments the caller may pull into (online). */
		environments: EnvTarget[];
		environmentId?: string;
		reference?: string;
		/** Called when the pull succeeded. */
		onpulled?: (environmentId: string, reference: string) => void;
	}

	let {
		open = $bindable(false),
		environments,
		environmentId = '',
		reference = '',
		onpulled
	}: Props = $props();
	const queryClient = useQueryClient();

	let env = $state('');
	let ref = $state('');
	let platform = $state('');
	let connection = $state('');
	let ready = $state(true);
	let busy = $state(false);
	let failure = $state<{ cause: unknown; refusal: Refusal } | null>(null);
	let jobId = $state<string | null>(null);
	let outcome = $state<Job | null>(null);

	$effect(() => {
		if (!open) return;
		untrack(() => {
			env = environmentId || environments[0]?.id || '';
			ref = reference;
			platform = '';
			connection = '';
			failure = null;
			jobId = null;
			outcome = null;
		});
	});

	const envName = $derived(environments.find((e) => e.id === env)?.name ?? '');
	const running = $derived(jobId !== null && outcome === null);

	async function pull() {
		busy = true;
		failure = null;
		try {
			const job = await unwrap(
				api.POST('/api/v1/environments/{environmentId}/images/pulls', {
					params: {
						path: { environmentId: env },
						header: { 'Idempotency-Key': idempotencyKey() }
					},
					body: {
						reference: ref.trim(),
						platform: platform.trim() || undefined,
						registryConnectionId: connection || undefined
					}
				})
			);
			jobId = job.id;
		} catch (e) {
			failure = {
				cause: e,
				refusal: refusal(e, {
					kind: 'image',
					name: ref.trim(),
					verb: 'pull',
					environmentName: envName
				})
			};
		} finally {
			busy = false;
		}
	}

	function finished(j: Job) {
		outcome = j;
		void queryClient.invalidateQueries({ queryKey: queryKeys.images.all });
		if (j.state === 'succeeded') {
			toast.success(doneTitle('pull', ref.trim()));
			onpulled?.(env, ref.trim());
		}
	}

	// JobProgress shows the job's own error; add what to do about registry failures.
	const guidance = $derived(
		outcome && outcome.state !== 'succeeded'
			? registryGuidance(outcome.error?.class)
			: undefined
	);
</script>

<Dialog bind:open title="Pull an image" size="md" dismissible={!busy}>
	<div class="form">
		{#if jobId}
			{#key jobId}
				<JobProgress {jobId} title="Pull {ref.trim()} on {envName}" onfinish={finished} />
			{/key}
			{#if guidance}
				<Notice tone="warn" title="What to do" live="alert">{guidance}</Notice>
			{/if}
		{:else}
			{#if environments.length > 1}
				<Select
					label="Environment"
					bind:value={env}
					options={environments.map((e) => ({ value: e.id, label: e.name }))}
				/>
			{:else if environments.length === 0}
				<Notice tone="warn" title="No environment to pull into" live="none">
					Pulling needs an online environment where you may pull images.
				</Notice>
			{/if}
			<TextField
				label="Image"
				mono
				required
				bind:value={ref}
				placeholder="nginx:1.27 or ghcr.io/acme/app:1.4"
				description="Without a tag, latest is pulled."
				autocomplete="off"
				spellcheck="false"
				error={fieldError(failure?.cause, 'body.reference')}
			/>
			<TextField
				label="Platform"
				mono
				bind:value={platform}
				placeholder="linux/arm64"
				description="Optional. Default: the environment's own platform."
				error={fieldError(failure?.cause, 'body.platform')}
			/>
			<RegistryMatchPreview
				reference={ref}
				environmentId={env}
				bind:selected={connection}
				onready={(r) => (ready = r)}
			/>
			{#if failure}
				<p class="error" role="alert">
					{failure.refusal.title}
					{failure.refusal.body ?? ''}
				</p>
			{/if}
		{/if}
	</div>
	{#snippet footer()}
		{#if jobId}
			<Button variant={running ? 'ghost' : 'primary'} onclick={() => (open = false)}
				>{running ? 'Continue in the background' : 'Done'}</Button
			>
		{:else}
			<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
			<Button
				variant="primary"
				icon={Download}
				loading={busy}
				disabled={!ref.trim() || !env || !ready}
				onclick={pull}>Pull image</Button
			>
		{/if}
	{/snippet}
</Dialog>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
