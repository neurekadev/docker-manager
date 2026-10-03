<script lang="ts">
	// Change a container in place (#6, #25: PATCH changes only the restart
	// policy and resource limits). Everything else needs a new container:
	// the dialog says so up front, listing the recreate-only settings the
	// server reports, instead of letting a request fail.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { queryKeys, type Container } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { Button, Dialog, Notice, Select, TextField, fieldError } from '$lib/ui';
	import { idempotencyKey, trackJob } from './jobs.svelte';
	import { megabytes, megabytesField, RESTART_OPTIONS } from './model';
	import { refusal, type Refusal } from './refusals';

	interface Props {
		open?: boolean;
		container: Container;
		environmentName?: string;
		/** Called with the change's job (the page shows its progress). */
		onstarted?: (job: Job) => void;
	}

	let { open = $bindable(false), container, environmentName, onstarted }: Props = $props();
	const queryClient = useQueryClient();

	let restart = $state('');
	let cpus = $state('');
	let memory = $state('');
	let pids = $state('');
	let busy = $state(false);
	let failure = $state<{ cause: unknown; refusal: Refusal } | null>(null);

	$effect(() => {
		if (!open) return;
		const d = container.details;
		restart = d?.restartPolicy || 'no';
		cpus = d?.resources.cpus ? String(d.resources.cpus) : '';
		memory = megabytesField(d?.resources.memoryBytes);
		pids = d?.resources.pidsLimit !== undefined ? String(d.resources.pidsLimit) : '';
		failure = null;
	});

	const FIELD_NAMES: Record<string, string> = {
		name: 'name',
		image: 'image',
		command: 'command',
		entrypoint: 'entrypoint',
		env: 'environment variables',
		labels: 'labels',
		workingDir: 'working directory',
		user: 'user',
		ports: 'ports',
		mounts: 'mounts',
		networks: 'networks',
		healthcheck: 'health check'
	};
	const recreate = $derived(
		(container.details?.recreate.fields ?? []).map((f) => FIELD_NAMES[f] ?? f)
	);

	const memBytes = $derived(megabytes(memory));
	const cpuNum = $derived(cpus.trim() === '' ? undefined : Number(cpus));
	const pidsNum = $derived(pids.trim() === '' ? undefined : Number(pids));
	const invalid = $derived({
		cpus:
			cpuNum !== undefined && (!Number.isFinite(cpuNum) || cpuNum < 0)
				? 'Enter a number of CPUs, e.g. 1.5.'
				: null,
		memory:
			memBytes !== undefined &&
			(Number.isNaN(memBytes) || (memBytes > 0 && memBytes < 6 * 1024 * 1024))
				? 'Enter at least 6 MB, or leave it empty for no limit.'
				: null,
		pids:
			pidsNum !== undefined && (!Number.isInteger(pidsNum) || pidsNum < -1)
				? 'Enter a whole number; -1 means unlimited.'
				: null
	});

	async function save() {
		busy = true;
		failure = null;
		try {
			const job = await unwrap(
				api.PATCH('/api/v1/environments/{environmentId}/containers/{containerId}', {
					params: {
						path: {
							environmentId: container.environmentId,
							containerId: container.name
						},
						header: { 'Idempotency-Key': idempotencyKey() }
					},
					body: {
						restartPolicy: restart as 'no' | 'always' | 'on-failure' | 'unless-stopped',
						// The limits replace the current ones: keep the settings this
						// dialog doesn't edit (CPU weight; swap while memory is unchanged).
						resources: {
							cpus: cpuNum ?? 0,
							cpuShares: container.details?.resources.cpuShares,
							memoryBytes: memBytes ?? 0,
							memorySwapBytes:
								(memBytes ?? 0) === (container.details?.resources.memoryBytes ?? 0)
									? container.details?.resources.memorySwapBytes
									: undefined,
							pidsLimit: pidsNum
						}
					}
				})
			);
			onstarted?.(job);
			trackJob(job, {
				ctx: { kind: 'container', name: container.name, verb: 'update' },
				queryClient,
				invalidate: [queryKeys.containers.all]
			});
			open = false;
		} catch (e) {
			failure = {
				cause: e,
				refusal: refusal(e, {
					kind: 'container',
					name: container.name,
					verb: 'update',
					protection: container.protection,
					environmentName
				})
			};
		} finally {
			busy = false;
		}
	}

	const shown = $derived(failure?.refusal ?? null);
	const cause = $derived(failure?.cause ?? null);
</script>

<Dialog bind:open title="Settings for {container.name}" size="md">
	<div class="form">
		<Select label="Restart Policy" bind:value={restart} options={[...RESTART_OPTIONS]} />
		<div class="grid">
			<TextField
				label="CPU Limit"
				inputmode="decimal"
				bind:value={cpus}
				placeholder="1.5"
				optional
				error={invalid.cpus ?? fieldError(cause, 'body.resources.cpus')}
			/>
			<TextField
				label="Memory Limit (MB)"
				inputmode="numeric"
				bind:value={memory}
				optional
				error={invalid.memory ?? fieldError(cause, 'body.resources.memoryBytes')}
			/>
			<TextField
				label="Process Limit"
				inputmode="numeric"
				bind:value={pids}
				optional
				description="-1 for unlimited."
				error={invalid.pids ?? fieldError(cause, 'body.resources.pidsLimit')}
			/>
		</div>
		{#if recreate.length}
			<Notice tone="info" title="Other Settings Need a New Container" live="none">
				Changing the {recreate.join(', ')} needs a new container or a
				<a href={routes.stacks()}>Compose stack</a>.
			</Notice>
		{/if}
		{#if shown}
			<p class="error" role="alert">{shown.title} {shown.body ?? ''}</p>
		{/if}
	</div>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button
			variant="primary"
			loading={busy}
			disabled={!!(invalid.cpus || invalid.memory || invalid.pids)}
			onclick={save}>Save Changes</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: var(--space-3);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}

	@media (max-width: 767px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}
</style>
