<script lang="ts">
	// Create a container (#6): the v1 form covers the common options only
	// (image, name, environment, ports, mounts, networks, restart policy,
	// resources; command, user, health check and labels folded under
	// "Advanced"); anything more belongs in a Compose stack. The image
	// field suggests the environment's images and takes any reference; it
	// must already be on the environment (#25: no implicit pull), so a
	// missing image offers a pull first. The create button stays in reach
	// (sticky footer) and says why it is not ready yet. Creations and pulls
	// running on the environment show above the form, from the running list,
	// so they come back after a reload.
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
		Notice,
		PageHeader,
		Select,
		SuggestField,
		TextArea,
		TextField,
		fieldError,
		toast
	} from '$lib/ui';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import PullImageDialog from '$lib/features/resources/PullImageDialog.svelte';
	import { idempotencyKey } from '$lib/features/resources/jobs.svelte';
	import { kindJobs } from '$lib/features/resources/object-jobs';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import {
		envLines,
		imagePresent,
		megabytes,
		NAME_RE,
		parsePairs,
		RESTART_OPTIONS,
		splitCommand
	} from '$lib/features/resources/model';
	import { doneTitle, jobFailure, refusal, type Refusal } from '$lib/features/resources/refusals';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({
		title: 'Create Container',
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
	// Why the create button is not ready yet (shown next to it).
	const blocker = $derived(
		!image.trim()
			? 'Enter an image.'
			: !name
				? 'Enter a name.'
				: !present
					? 'Pull the image first.'
					: Object.values(errors).some(Boolean)
						? 'Fix the fields marked in red.'
						: null
	);
	// The advanced section opens by itself when one of its fields has an error.
	const advancedError = $derived(
		!!(errors.command || errors.entrypoint || errors.health || errors.labels)
	);
	// The environment's images as suggestions (tags; any reference or ID still works).
	const imageSuggestions = $derived(
		[...new Set((images.data?.items ?? []).flatMap((im) => im.repoTags))].sort((a, b) =>
			a.localeCompare(b)
		)
	);

	let busy = $state(false);
	let failure = $state<{ cause: unknown; refusal: Refusal } | null>(null);
	let outcome = $state<Refusal | null>(null);
	// Creations and pulls on the environment: the one this form started
	// (`started`: the form gives way to its progress) and any found running.
	const jobs = useTrackedJobs(() =>
		env ? kindJobs(['container.create', 'image.pull'], env) : null
	);
	let started = $state<string | null>(null);

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
			jobs.add(job, `Create ${name} on ${scope.name(env)}`);
			started = job.id;
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
		if (j.kind === 'image.pull') {
			if (j.state === 'succeeded') void images.refetch();
			return;
		}
		void queryClient.invalidateQueries({ queryKey: queryKeys.containers.all });
		if (j.id !== started) return;
		if (j.state === 'succeeded') {
			toast.success(doneTitle('create', name));
			void goto(routes.container(env, name));
		} else {
			outcome = jobFailure(j, { kind: 'container', name, verb: 'create' });
		}
	}

	function backToForm() {
		if (started) jobs.dismiss(started);
		started = null;
		outcome = null;
	}

	const networkOptions = $derived([
		{ value: '', label: 'Choose a Network' },
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
	onstarted={(job, title) => jobs.add(job, title)}
/>

{#if scope.ready && scope.envs.data && !allowed.length}
	<DeniedState
		level={1}
		title="You can't create containers here."
		description="Creating containers needs the permission on an online environment. Ask the owner of this Docker Manager if you need it."
	/>
{:else}
	<Page narrow>
		<PageHeader
			title="Create Container"
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

		<ActiveJobs {jobs} onfinish={finished} />
		{#if started}
			{#if outcome}
				<Notice tone="danger" title={outcome.title} live="alert">
					{outcome.body}
					{#snippet actions()}
						<Button variant="secondary" onclick={backToForm}>Back to the Form</Button>
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
				<Card title="Image and Name">
					<div class="fields two">
						{#if allowed.length > 1}
							<Select
								label="Environment"
								bind:value={env}
								options={allowed.map((e) => ({ value: e.id, label: e.name }))}
							/>
						{/if}
						<SuggestField
							label="Image"
							mono
							required
							bind:value={image}
							suggestions={imageSuggestions}
							placeholder="repository:tag"
							description="An image on {scope.name(
								env
							)}: pick one or type a reference or image ID."
							error={fieldError(failure?.cause, 'body.image')}
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
											onclick={() => (pullOpen = true)}>Pull Image</Button
										>
									{/if}
								{/snippet}
							</Notice>
						</div>
					{/if}
				</Card>

				<Card title="Environment Variables">
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
								label="Host Port"
								inputmode="numeric"
								bind:value={p.host}
								placeholder="any"
							/>
							<TextField
								label="Container Port"
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
								label="Host Address"
								mono
								bind:value={p.ip}
								placeholder="all addresses"
							/>
							<IconButton
								icon={Trash2}
								label="Remove Port {i + 1}"
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
						>Add Port</Button
					>
				</Card>

				<Card title="Volumes and Host Paths">
					{#each mounts as m, i (i)}
						<div class="row mounts">
							<Select
								label="Type"
								bind:value={m.type}
								options={[
									{ value: 'volume', label: 'Volume' },
									{ value: 'bind', label: 'Host Path' },
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
									label="Host Path"
									mono
									bind:value={m.source}
									placeholder="/srv/data"
								/>
							{/if}
							<TextField
								label="Path in the Container"
								mono
								required
								bind:value={m.target}
								placeholder="/data"
							/>
							<Checkbox label="Read-Only" bind:checked={m.readOnly} />
							<IconButton
								icon={Trash2}
								label="Remove Mount {i + 1}"
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
							})}>Add Mount</Button
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
								label="Remove Network {i + 1}"
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
						onclick={() => nets.push({ name: '', aliases: '' })}>Add Network</Button
					>
				</Card>

				<Card title="Restart and Limits">
					<div class="fields three">
						<Select
							label="Restart Policy"
							bind:value={restart}
							description="When Docker starts the container again on its own."
							options={[...RESTART_OPTIONS]}
						/>
						<TextField
							label="CPU Limit"
							inputmode="decimal"
							bind:value={cpus}
							description="CPUs. Optional."
							error={errors.cpus}
						/>
						<TextField
							label="Memory Limit (MB)"
							inputmode="numeric"
							bind:value={memory}
							description="Optional."
							error={errors.memory}
						/>
					</div>
				</Card>

				<Card title="Advanced">
					<Disclosure
						summary="Command, User, Health Check and Labels"
						open={advancedError}
					>
						<div class="advanced">
							<h3 class="subsection-title">Command</h3>
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
									label="Working Directory"
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
							<h3 class="subsection-title">Health Check</h3>
							<div class="fields three">
								<TextField
									label="Health Check Command"
									mono
									bind:value={healthCmd}
									placeholder="curl -f http://localhost/"
									description="Optional. Runs in the container; exit code 0 is healthy."
									error={errors.health}
								/>
								<TextField
									label="Check Every (Seconds)"
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
							<TextArea
								label="Labels"
								mono
								rows={3}
								bind:value={labelText}
								placeholder="traefik.enable=true"
								description="One key=value per line. Labels of Docker Manager and Compose are reserved."
								error={errors.labels ?? fieldError(failure?.cause, 'body.labels')}
							/>
						</div>
					</Disclosure>
				</Card>

				{#if failure}
					<Notice tone="danger" title={failure.refusal.title} live="alert"
						>{failure.refusal.body}</Notice
					>
				{/if}
				<div class="submit">
					<Checkbox label="Start It After Creating" bind:checked={start} />
					{#if blocker}<p class="blocker" aria-live="polite">{blocker}</p>{/if}
					<div class="buttons">
						<Button variant="ghost" href={routes.containers()}>Cancel</Button>
						<Button
							type="submit"
							variant="primary"
							icon={Plus}
							loading={busy}
							disabled={!valid}>Create Container</Button
						>
					</div>
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

	.advanced {
		display: grid;
		gap: var(--space-4);
		margin-top: var(--space-2);
	}

	/* Sticky: the create button stays in reach on a long form. */
	.submit {
		position: sticky;
		bottom: 0;
		z-index: var(--z-sticky);
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: flex-end;
		gap: var(--space-3);
		padding: var(--space-3) 0;
		border-top: 1px solid var(--border-subtle);
		background: var(--surface-canvas);
	}

	.blocker {
		margin: 0;
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.submit :global(> :first-child) {
		margin-right: auto;
	}

	/* Cancel and Create wrap together (on a phone: below the switch). */
	.buttons {
		display: flex;
		gap: var(--space-3);
		margin-left: auto;
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
