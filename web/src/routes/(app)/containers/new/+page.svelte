<script lang="ts">
	// Create a container (#6): the v1 form covers the common options only
	// (image, name, command, environment, ports, mounts, networks, restart
	// policy, labels, resources, health check); anything more belongs in a
	// Compose stack. The image must already be on the environment (#25: no
	// implicit pull), so a missing image offers a pull first.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Download from '@lucide/svelte/icons/download';
	import Layers from '@lucide/svelte/icons/layers';
	import Plus from '@lucide/svelte/icons/plus';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { imagesQuery, networksQuery, queryKeys, volumesQuery } from '$lib/api/queries';
	import { criticalWork } from '$lib/live';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		Checkbox,
		DeniedState,
		IconButton,
		JobProgress,
		Notice,
		PageHeader,
		Select,
		SuggestField,
		Switch,
		TextArea,
		TextField,
		fieldError,
		toast
	} from '$lib/ui';
	import Page from '$lib/features/resources/Page.svelte';
	import PullImageDialog from '$lib/features/resources/PullImageDialog.svelte';
	import { idempotencyKey } from '$lib/features/resources/jobs.svelte';
	import {
		envLines,
		imagePresent,
		megabytes,
		NAME_RE,
		parsePairs,
		splitCommand
	} from '$lib/features/resources/model';
	import { doneTitle, jobFailure, refusal, type Refusal } from '$lib/features/resources/refusals';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({
		title: 'Create container',
		crumbs: [{ label: 'Containers', href: routes.containers() }, { label: 'Create' }],
		environmentScoped: true
	});

	const CMD_PLACEHOLDER = 'nginx -g ' + JSON.stringify('daemon off;');
	const ENV_PLACEHOLDER = ['TZ=Europe/Berlin', 'LOG_LEVEL=info'].join('\n');
	const queryClient = useQueryClient();
	const scope = useEnvironmentScope();
	const allowed = $derived(scope.creatable('container.create'));
	const pullable = $derived(scope.creatable('image.pull'));

	let env = $state('');
	$effect(() => {
		if (env || !allowed.length) return;
		const want = page.url.searchParams.get('environment');
		env = allowed.find((e) => e.id === want)?.id ?? allowed[0].id;
	});
	const target = $derived(allowed.filter((e) => e.id === env));

	// Form state.
	let image = $state(untrack(() => page.url.searchParams.get('image') ?? ''));
	let name = $state('');
	let command = $state('');
	let entrypoint = $state('');
	let workingDir = $state('');
	let user = $state('');
	let envText = $state('');
	let labelText = $state('');
	let restart = $state('unless-stopped');
	let cpus = $state('');
	let memory = $state('');
	let healthCmd = $state('');
	let healthInterval = $state('');
	let healthRetries = $state('');
	let start = $state(true);
	type PortRow = { host: string; container: string; protocol: string; ip: string };
	type MountRow = { type: string; source: string; target: string; readOnly: boolean };
	type NetRow = { name: string; aliases: string };
	let ports = $state<PortRow[]>([]);
	let mounts = $state<MountRow[]>([]);
	let nets = $state<NetRow[]>([]);

	// Unsaved form: keep a PWA update from reloading it away (#23).
	let release: (() => void) | null = null;
	$effect(() => {
		const dirty = !!(name || command || envText || ports.length || mounts.length);
		if (dirty && !release)
			release = criticalWork.register('unsaved-edit', 'the container form');
		if (!dirty && release) {
			release();
			release = null;
		}
	});
	$effect(() => () => release?.());

	const images = createQuery(() => ({ ...imagesQuery(target), enabled: target.length === 1 }));
	const networks = createQuery(() => ({
		...networksQuery(target),
		enabled: target.length === 1
	}));
	const volumes = createQuery(() => ({ ...volumesQuery(target), enabled: target.length === 1 }));
	const present = $derived(
		!image.trim() || !images.data ? true : imagePresent(image, images.data.items)
	);
	let pullOpen = $state(false);

	// Client-side checks (the server validates everything again).
	const envParsed = $derived(envLines(envText));
	const labelParsed = $derived(parsePairs(labelText));
	const cmdArgs = $derived(command.trim() ? splitCommand(command) : []);
	const epArgs = $derived(entrypoint.trim() ? splitCommand(entrypoint) : []);
	const healthArgs = $derived(healthCmd.trim() ? splitCommand(healthCmd) : []);
	const memBytes = $derived(megabytes(memory));
	const portNum = (s: string) => (s.trim() === '' ? 0 : Number(s));
	const errors = $derived({
		name:
			name && !NAME_RE.test(name)
				? 'Use letters, digits, ".", "_" and "-", starting with a letter or digit.'
				: null,
		command: cmdArgs === null ? 'A quote is not closed.' : null,
		entrypoint: epArgs === null ? 'A quote is not closed.' : null,
		health: healthArgs === null ? 'A quote is not closed.' : null,
		env: envParsed.invalid.length
			? `Line ${envParsed.invalid.join(', ')}: use KEY=value.`
			: null,
		labels: labelParsed.invalid.length
			? `Line ${labelParsed.invalid.join(', ')}: use key=value.`
			: null,
		cpus: cpus.trim() && !(Number(cpus) > 0) ? 'Enter a number of CPUs, e.g. 1.5.' : null,
		memory:
			memBytes !== undefined && (Number.isNaN(memBytes) || memBytes < 6 * 1024 * 1024)
				? 'Enter at least 6 MB, or leave it empty.'
				: null,
		ports: ports.some((p) => {
			const c = portNum(p.container);
			const h = portNum(p.host);
			return (
				!Number.isInteger(c) ||
				c < 1 ||
				c > 65535 ||
				!Number.isInteger(h) ||
				h < 0 ||
				h > 65535
			);
		})
			? 'Ports are numbers from 1 to 65535 (host port empty: any free port).'
			: null,
		mounts: mounts.some(
			(m) => !m.target.startsWith('/') || (m.type === 'bind' && !m.source.startsWith('/'))
		)
			? 'Paths in the container are absolute; bind mounts need an absolute host path.'
			: null
	});
	const valid = $derived(
		!!image.trim() && !!name && Object.values(errors).every((e) => !e) && present
	);

	let busy = $state(false);
	let failure = $state<{ cause: unknown; refusal: Refusal } | null>(null);
	let jobId = $state<string | null>(null);
	let outcome = $state<Refusal | null>(null);

	async function create() {
		busy = true;
		failure = null;
		try {
			const job = await unwrap(
				api.POST('/api/v1/environments/{environmentId}/containers', {
					params: {
						path: { environmentId: env },
						header: { 'Idempotency-Key': idempotencyKey() }
					},
					body: {
						name,
						image: image.trim(),
						command: cmdArgs?.length ? cmdArgs : undefined,
						entrypoint: epArgs?.length ? epArgs : undefined,
						env: envParsed.values.length ? envParsed.values : undefined,
						labels: Object.keys(labelParsed.values).length
							? labelParsed.values
							: undefined,
						workingDir: workingDir.trim() || undefined,
						user: user.trim() || undefined,
						ports: ports.length
							? ports.map((p) => ({
									containerPort: portNum(p.container),
									hostPort: portNum(p.host) || undefined,
									protocol: p.protocol as 'tcp' | 'udp',
									hostIp: p.ip.trim() || undefined
								}))
							: undefined,
						mounts: mounts.length
							? mounts.map((m) => ({
									type: m.type as 'bind' | 'volume' | 'tmpfs',
									source: m.source.trim() || undefined,
									target: m.target.trim(),
									readOnly: m.readOnly || undefined
								}))
							: undefined,
						networks: nets.filter((n) => n.name).length
							? nets
									.filter((n) => n.name)
									.map((n) => ({
										name: n.name,
										aliases: n.aliases.trim()
											? n.aliases.split(/[\s,]+/).filter(Boolean)
											: undefined
									}))
							: undefined,
						restartPolicy: restart as 'no' | 'always' | 'on-failure' | 'unless-stopped',
						resources:
							cpus.trim() || memBytes
								? {
										cpus: cpus.trim() ? Number(cpus) : undefined,
										memoryBytes: memBytes || undefined
									}
								: undefined,
						healthcheck: healthArgs?.length
							? {
									test: ['CMD', ...healthArgs],
									intervalSeconds: healthInterval
										? Number(healthInterval)
										: undefined,
									retries: healthRetries ? Number(healthRetries) : undefined
								}
							: undefined,
						start
					}
				})
			);
			jobId = job.id;
			release?.();
			release = null;
		} catch (e) {
			failure = {
				cause: e,
				refusal: refusal(e, {
					kind: 'container',
					name,
					verb: 'create',
					environmentName: scope.name(env)
				})
			};
		} finally {
			busy = false;
		}
	}

	function finished(j: Job) {
		void queryClient.invalidateQueries({ queryKey: queryKeys.containers.all });
		if (j.state === 'succeeded') {
			toast.success(doneTitle('create', name));
			void goto(routes.container(env, name));
		} else {
			outcome = jobFailure(j, { kind: 'container', name, verb: 'create' });
		}
	}

	const networkOptions = $derived([
		{ value: '', label: 'Choose a network' },
		...(networks.data?.items ?? []).map((n) => ({ value: n.name, label: n.name }))
	]);
	const volumeNames = $derived(
		(volumes.data?.items ?? []).filter((v) => !v.protection).map((v) => v.name)
	);
</script>

<PullImageDialog
	bind:open={pullOpen}
	environments={pullable.filter((e) => e.id === env)}
	environmentId={env}
	reference={image}
	onpulled={() => images.refetch()}
/>

{#if scope.ready && scope.envs.data && !allowed.length}
	<DeniedState
		level={1}
		title="You can't create containers here."
		description="Creating containers needs the permission on an online environment. Ask the owner of this Docker Manager if you need it."
	/>
{:else}
	<Page>
		<PageHeader
			title="Create container"
			description="One container from an image that is already on the environment."
		/>
		<Notice
			tone="info"
			icon={Layers}
			title="Several services, builds or shared networks?"
			live="none"
		>
			Use a <a href={routes.stacks()}>Compose stack</a>: it keeps the whole setup in one file
			you can edit, deploy and back up. This form covers the common options only.
		</Notice>

		{#if jobId}
			<JobProgress {jobId} title="Create {name} on {scope.name(env)}" onfinish={finished} />
			{#if outcome}
				<Notice tone="danger" title={outcome.title} live="alert">
					{outcome.body}
					{#snippet actions()}
						<Button
							variant="secondary"
							onclick={() => ((jobId = null), (outcome = null))}
							>Back to the form</Button
						>
					{/snippet}
				</Notice>
			{/if}
		{:else}
			<form
				class="form"
				onsubmit={(e) => {
					e.preventDefault();
					if (valid) void create();
				}}
			>
				<Card title="Image and name">
					<div class="fields two">
						{#if allowed.length > 1}
							<Select
								label="Environment"
								bind:value={env}
								options={allowed.map((e) => ({ value: e.id, label: e.name }))}
							/>
						{/if}
						<TextField
							label="Image"
							mono
							required
							bind:value={image}
							placeholder="repository:tag"
							description="A reference or image ID. It must already be on {scope.name(
								env
							)}."
							error={fieldError(failure?.cause, 'body.image')}
							autocomplete="off"
							spellcheck="false"
						/>
						<TextField
							label="Name"
							mono
							required
							bind:value={name}
							placeholder="web"
							error={errors.name ?? fieldError(failure?.cause, 'body.name')}
							autocomplete="off"
							spellcheck="false"
						/>
					</div>
					{#if !present}
						<div class="missing">
							<Notice
								tone="warn"
								title="{image.trim()} is not on {scope.name(env)}"
								live="status"
							>
								Creating a container never downloads images. Pull it first, then
								create the container.
								{#snippet actions()}
									{#if pullable.some((e) => e.id === env)}
										<Button
											variant="secondary"
											icon={Download}
											onclick={() => (pullOpen = true)}>Pull image</Button
										>
									{/if}
								{/snippet}
							</Notice>
						</div>
					{/if}
				</Card>

				<Card title="Command">
					<div class="fields two">
						<TextField
							label="Command"
							mono
							bind:value={command}
							placeholder={CMD_PLACEHOLDER}
							description="Optional. Default: the image's command."
							error={errors.command}
						/>
						<TextField
							label="Entrypoint"
							mono
							bind:value={entrypoint}
							description="Optional. Default: the image's entrypoint."
							error={errors.entrypoint}
						/>
						<TextField
							label="Working directory"
							mono
							bind:value={workingDir}
							description="Optional."
						/>
						<TextField
							label="User"
							mono
							bind:value={user}
							placeholder="1000:1000"
							description="Optional."
						/>
					</div>
				</Card>

				<Card title="Environment variables">
					<TextArea
						label="Variables"
						mono
						rows={4}
						bind:value={envText}
						placeholder={ENV_PLACEHOLDER}
						description="One KEY=value per line. Docker Manager stores the values sealed and never shows them again; the container page lists the names only."
						error={errors.env}
					/>
				</Card>

				<Card title="Ports">
					{#each ports as p, i (i)}
						<div class="row ports">
							<TextField
								label="Host port"
								inputmode="numeric"
								bind:value={p.host}
								placeholder="any"
							/>
							<TextField
								label="Container port"
								inputmode="numeric"
								required
								bind:value={p.container}
							/>
							<Select
								label="Protocol"
								bind:value={p.protocol}
								options={[
									{ value: 'tcp', label: 'TCP' },
									{ value: 'udp', label: 'UDP' }
								]}
							/>
							<TextField
								label="Host address"
								mono
								bind:value={p.ip}
								placeholder="all addresses"
							/>
							<IconButton
								icon={Trash2}
								label="Remove port {i + 1}"
								onclick={() => ports.splice(i, 1)}
							/>
						</div>
					{/each}
					{#if errors.ports}<p class="err" role="alert">{errors.ports}</p>{/if}
					<Button
						variant="secondary"
						size="sm"
						icon={Plus}
						onclick={() =>
							ports.push({ host: '', container: '', protocol: 'tcp', ip: '' })}
						>Add port</Button
					>
				</Card>

				<Card title="Volumes and host paths">
					{#each mounts as m, i (i)}
						<div class="row mounts">
							<Select
								label="Type"
								bind:value={m.type}
								options={[
									{ value: 'volume', label: 'Volume' },
									{ value: 'bind', label: 'Host path' },
									{ value: 'tmpfs', label: 'Memory (tmpfs)' }
								]}
							/>
							{#if m.type === 'volume'}
								<SuggestField
									label="Volume"
									mono
									bind:value={m.source}
									suggestions={volumeNames}
									placeholder="new or existing volume"
								/>
							{:else if m.type === 'bind'}
								<TextField
									label="Host path"
									mono
									bind:value={m.source}
									placeholder="/srv/data"
								/>
							{/if}
							<TextField
								label="Path in the container"
								mono
								required
								bind:value={m.target}
								placeholder="/data"
							/>
							<Checkbox label="Read-only" bind:checked={m.readOnly} />
							<IconButton
								icon={Trash2}
								label="Remove mount {i + 1}"
								onclick={() => mounts.splice(i, 1)}
							/>
						</div>
					{/each}
					{#if errors.mounts}<p class="err" role="alert">{errors.mounts}</p>{/if}
					<p class="hint">
						Docker Manager's own volumes and the Docker socket cannot be mounted. A
						volume that doesn't exist yet is created.
					</p>
					<Button
						variant="secondary"
						size="sm"
						icon={Plus}
						onclick={() =>
							mounts.push({
								type: 'volume',
								source: '',
								target: '',
								readOnly: false
							})}>Add mount</Button
					>
				</Card>

				<Card title="Networks">
					{#each nets as n, i (i)}
						<div class="row nets">
							<Select label="Network" bind:value={n.name} options={networkOptions} />
							<TextField
								label="Aliases"
								mono
								bind:value={n.aliases}
								placeholder="api, backend"
								description="Optional."
							/>
							<IconButton
								icon={Trash2}
								label="Remove network {i + 1}"
								onclick={() => nets.splice(i, 1)}
							/>
						</div>
					{/each}
					<p class="hint">
						Without a network the container joins Docker's default bridge.
					</p>
					<Button
						variant="secondary"
						size="sm"
						icon={Plus}
						onclick={() => nets.push({ name: '', aliases: '' })}>Add network</Button
					>
				</Card>

				<Card title="Restart, limits and health">
					<div class="fields three">
						<Select
							label="Restart policy"
							bind:value={restart}
							options={[
								{ value: 'no', label: 'Never (no)' },
								{ value: 'on-failure', label: 'When it fails (on-failure)' },
								{
									value: 'unless-stopped',
									label: 'Unless stopped (unless-stopped)'
								},
								{ value: 'always', label: 'Always (always)' }
							]}
						/>
						<TextField
							label="CPU limit"
							inputmode="decimal"
							bind:value={cpus}
							description="CPUs. Optional."
							error={errors.cpus}
						/>
						<TextField
							label="Memory limit (MB)"
							inputmode="numeric"
							bind:value={memory}
							description="Optional."
							error={errors.memory}
						/>
						<TextField
							label="Health check command"
							mono
							bind:value={healthCmd}
							placeholder="curl -f http://localhost/"
							description="Optional. Runs in the container; exit code 0 is healthy."
							error={errors.health}
						/>
						<TextField
							label="Check every (seconds)"
							inputmode="numeric"
							bind:value={healthInterval}
							description="Optional."
						/>
						<TextField
							label="Retries"
							inputmode="numeric"
							bind:value={healthRetries}
							description="Optional."
						/>
					</div>
				</Card>

				<Card title="Labels">
					<TextArea
						label="Labels"
						mono
						rows={3}
						bind:value={labelText}
						placeholder="traefik.enable=true"
						description="One key=value per line. dev.neureka.docker-manager.* and com.docker.compose.* are reserved."
						error={errors.labels ?? fieldError(failure?.cause, 'body.labels')}
					/>
				</Card>

				{#if failure}
					<Notice tone="danger" title={failure.refusal.title} live="alert"
						>{failure.refusal.body}</Notice
					>
				{/if}
				<div class="submit">
					<Switch label="Start it after creating" bind:checked={start} />
					<Button variant="ghost" href={routes.containers()}>Cancel</Button>
					<Button
						type="submit"
						variant="primary"
						icon={Plus}
						loading={busy}
						disabled={!valid}>Create container</Button
					>
				</div>
			</form>
		{/if}
	</Page>
{/if}

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.fields {
		display: grid;
		gap: var(--space-4);
	}

	.two {
		grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
	}

	.three {
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
	}

	.row {
		display: grid;
		align-items: end;
		gap: var(--space-3);
		margin-bottom: var(--space-3);
	}

	.ports {
		grid-template-columns: 1fr 1fr 110px 1.4fr auto;
	}

	.mounts {
		grid-template-columns: 150px 1.4fr 1.4fr auto auto;
	}

	.nets {
		grid-template-columns: 1fr 1.4fr auto;
	}

	.mounts :global(.dy-checkbox),
	.mounts :global(label) {
		white-space: nowrap;
	}

	.missing {
		margin-top: var(--space-4);
	}

	.hint {
		margin: 0 0 var(--space-3);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.err {
		margin: 0 0 var(--space-3);
		color: var(--danger);
		font-size: var(--text-caption);
	}

	.submit {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: flex-end;
		gap: var(--space-3);
	}

	.submit :global(> :first-child) {
		margin-right: auto;
	}

	@media (max-width: 767px) {
		.ports,
		.mounts,
		.nets {
			grid-template-columns: 1fr;
			padding-bottom: var(--space-3);
			border-bottom: 1px solid var(--border-subtle);
		}
	}
</style>
