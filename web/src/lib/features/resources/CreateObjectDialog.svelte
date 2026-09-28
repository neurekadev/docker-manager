<script lang="ts">
	// Create a volume or a network (#6): both are jobs; the name is checked
	// here (Docker's name rules) and again on the server (409
	// resource_name_taken when it exists).
	import { untrack } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap } from '$lib/api/client';
	import type { EnvTarget } from '$lib/api/multi-env';
	import { queryKeys } from '$lib/api/queries';
	import {
		Button,
		Checkbox,
		Dialog,
		Notice,
		Select,
		TextArea,
		TextField,
		fieldError
	} from '$lib/ui';
	import { idempotencyKey, trackJob } from './jobs.svelte';
	import { NAME_RE, parsePairs } from './model';
	import { refusal, type Refusal } from './refusals';

	interface Props {
		open?: boolean;
		kind: 'volume' | 'network';
		environments: EnvTarget[];
		environmentId?: string;
	}

	let { open = $bindable(false), kind, environments, environmentId }: Props = $props();
	const queryClient = useQueryClient();

	let env = $state('');
	let name = $state('');
	let driver = $state('');
	let optionsText = $state('');
	let labelsText = $state('');
	let internal = $state(false);
	let attachable = $state(false);
	let busy = $state(false);
	let failure = $state<{ cause: unknown; refusal: Refusal } | null>(null);

	$effect(() => {
		if (!open) return;
		untrack(() => {
			env =
				environmentId && environments.some((e) => e.id === environmentId)
					? environmentId
					: (environments[0]?.id ?? '');
			name = driver = optionsText = labelsText = '';
			internal = attachable = false;
			failure = null;
		});
	});

	const opts = $derived(parsePairs(optionsText));
	const labels = $derived(parsePairs(labelsText));
	const nameError = $derived(
		name && !NAME_RE.test(name)
			? 'Use letters, digits, ".", "_" and "-", starting with a letter or digit.'
			: null
	);
	const valid = $derived(
		!!env && !!name && !nameError && !opts.invalid.length && !labels.invalid.length
	);
	const envName = $derived(environments.find((e) => e.id === env)?.name ?? '');

	async function create() {
		busy = true;
		failure = null;
		const header = { 'Idempotency-Key': idempotencyKey() };
		const common = {
			name,
			driver: driver.trim() || undefined,
			labels: Object.keys(labels.values).length ? labels.values : undefined
		};
		try {
			const job =
				kind === 'volume'
					? await unwrap(
							api.POST('/api/v1/environments/{environmentId}/volumes', {
								params: { path: { environmentId: env }, header },
								body: {
									...common,
									driverOpts: Object.keys(opts.values).length
										? opts.values
										: undefined
								}
							})
						)
					: await unwrap(
							api.POST('/api/v1/environments/{environmentId}/networks', {
								params: { path: { environmentId: env }, header },
								body: {
									...common,
									internal: internal || undefined,
									attachable: attachable || undefined,
									options: Object.keys(opts.values).length
										? opts.values
										: undefined
								}
							})
						);
			trackJob(job, {
				ctx: { kind, name, verb: 'create', environmentName: envName },
				queryClient,
				invalidate: [kind === 'volume' ? queryKeys.volumes.all : queryKeys.networks.all]
			});
			open = false;
		} catch (e) {
			failure = {
				cause: e,
				refusal: refusal(e, { kind, name, verb: 'create', environmentName: envName })
			};
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title={kind === 'volume' ? 'Create a volume' : 'Create a network'}
	size="md"
	dismissible={!busy}
>
	<form
		class="form"
		id="create-{kind}"
		onsubmit={(e) => {
			e.preventDefault();
			if (valid) void create();
		}}
	>
		{#if environments.length > 1}
			<Select
				label="Environment"
				bind:value={env}
				options={environments.map((e) => ({ value: e.id, label: e.name }))}
			/>
		{:else if environments.length === 0}
			<Notice tone="warn" title="No environment to create it in" live="none">
				Creating a {kind} needs an online environment where you have the permission.
			</Notice>
		{/if}
		<TextField
			label="Name"
			mono
			required
			bind:value={name}
			placeholder={kind === 'volume' ? 'app_data' : 'backend'}
			error={nameError ?? fieldError(failure?.cause, 'body.name')}
			autocomplete="off"
			spellcheck="false"
		/>
		<TextField
			label="Driver"
			mono
			bind:value={driver}
			placeholder={kind === 'volume' ? 'local' : 'bridge'}
			description={kind === 'volume'
				? 'Optional. Only local volumes support files, watching and backups in Docker Manager.'
				: 'Optional. Default bridge.'}
		/>
		{#if kind === 'network'}
			<div class="checks">
				<Checkbox
					label="Internal"
					description="No access to or from outside networks."
					bind:checked={internal}
				/>
				<Checkbox
					label="Attachable"
					description="Standalone containers may join it."
					bind:checked={attachable}
				/>
			</div>
		{/if}
		<TextArea
			label="Driver options"
			mono
			rows={2}
			bind:value={optionsText}
			description="Optional. One key=value per line."
			error={opts.invalid.length ? `Line ${opts.invalid.join(', ')}: use key=value.` : null}
		/>
		<TextArea
			label="Labels"
			mono
			rows={2}
			bind:value={labelsText}
			description="Optional. One key=value per line; docker-manager.* (except the *.exclude labels) and com.docker.compose.* are reserved."
			error={labels.invalid.length
				? `Line ${labels.invalid.join(', ')}: use key=value.`
				: fieldError(failure?.cause, 'body.labels')}
		/>
		{#if failure}
			<p class="error" role="alert">{failure.refusal.title} {failure.refusal.body ?? ''}</p>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button
			type="submit"
			form="create-{kind}"
			variant="primary"
			loading={busy}
			disabled={!valid}>{kind === 'volume' ? 'Create volume' : 'Create network'}</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.checks {
		display: grid;
		grid-template-columns: 1fr 1fr;
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
		.checks {
			grid-template-columns: 1fr;
		}
	}
</style>
