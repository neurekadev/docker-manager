<script lang="ts">
	// Add environment (#3) and re-attach an archived one (#34): create a
	// one-use enrollment token (POST /agent-enrollments, intent new or
	// reattach:<id>), show the generated install commands and the token
	// once, then follow the enrollment until the agent connects. The token
	// lives only in this page's memory; while it is shown, the PWA update
	// prompt will not reload the page (criticalWork).
	import { onDestroy } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Plus from '@lucide/svelte/icons/plus';
	import { api, unwrap, unwrapEmpty, type Schema } from '$lib/api/client';
	import {
		enrollmentsQuery,
		environmentQuery,
		environmentsQuery,
		myPermissionsQuery
	} from '$lib/api/queries';
	import InstallCommand from '$lib/features/environments/InstallCommand.svelte';
	import {
		INSTALL_VARIANTS,
		REJECTIONS,
		TOKEN_LIFETIMES
	} from '$lib/features/environments/enrollment';
	import { criticalWork, liveKeys } from '$lib/live';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		Checkbox,
		DeniedState,
		ErrorState,
		Notice,
		PageHeader,
		SecretReveal,
		Select,
		Spinner,
		StatusBadge,
		Tabs,
		TextField,
		errorMessage,
		fieldError,
		formatDateTime,
		formatRelative,
		toast
	} from '$lib/ui';

	type Created = Schema<'CreatedAgentEnrollment'>;

	const reattachId = $derived(page.url.searchParams.get('reattach') ?? '');
	const reattach = createQuery(() => ({
		...environmentQuery(reattachId),
		enabled: !!reattachId
	}));
	const title = $derived(reattachId ? 'Re-attach environment' : 'Add environment');

	usePage(() => ({
		title,
		crumbs: [{ label: 'Environments', href: routes.environments() }, { label: title }]
	}));

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const canEnroll = $derived(access.owner || access.allowed.has('agent.enroll'));

	let name = $state('');
	let lifetime = $state('3600');
	let duplicate = $state(false);
	let creating = $state(false);
	let error = $state<unknown>(null);
	let created = $state<Created | null>(null);
	let showSecrets = $state(true);
	let variant = $state('remote');
	let releaseCritical: (() => void) | null = null;

	// Follow the enrollment until it is used, refused or expires.
	const enrollments = createQuery(() => ({
		...enrollmentsQuery(),
		enabled: !!created,
		refetchInterval: 3000
	}));
	const envs = createQuery(() => ({ ...environmentsQuery(), enabled: !!created }));
	const current = $derived(
		created
			? (enrollments.data?.find((e) => e.id === created!.enrollment.id) ?? created.enrollment)
			: null
	);
	const enrolledEnv = $derived(
		current?.agentId ? envs.data?.find((e) => e.agentId === current.agentId) : undefined
	);
	const commands = $derived(
		[...(created?.installCommands ?? [])].sort(
			(a, b) =>
				(INSTALL_VARIANTS[a.variant]?.order ?? 9) -
				(INSTALL_VARIANTS[b.variant]?.order ?? 9)
		)
	);
	const commandTabs = $derived(
		commands.map((c) => ({
			id: c.variant,
			label: INSTALL_VARIANTS[c.variant]?.heading ?? c.title
		}))
	);

	$effect(() => {
		// The token is no longer needed once the agent enrolled or it ended.
		if (current && current.state !== 'pending') {
			releaseCritical?.();
			releaseCritical = null;
		}
		if (current?.state === 'used') {
			void qc.invalidateQueries({ queryKey: liveKeys.list('environments') });
		}
	});
	onDestroy(() => releaseCritical?.());

	async function create(ev: SubmitEvent) {
		ev.preventDefault();
		creating = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/agent-enrollments', {
					params: { header: { 'Idempotency-Key': crypto.randomUUID() } },
					body: {
						intent: reattachId ? `reattach:${reattachId}` : 'new',
						environmentName: reattachId ? undefined : name.trim() || undefined,
						expiresInSeconds: Number(lifetime),
						allowDuplicateEngineId: reattachId ? undefined : duplicate || undefined
					}
				})
			);
			created = out;
			showSecrets = true;
			variant = out.installCommands.some((c) => c.variant === 'remote')
				? 'remote'
				: (out.installCommands[0]?.variant ?? '');
			releaseCritical = criticalWork.register('other', 'enrollment token');
			void qc.invalidateQueries({ queryKey: liveKeys.list('agents') });
			toast.success('Created enrollment token', {
				body: `It works once and expires ${formatRelative(out.enrollment.expiresAt)}.`
			});
		} catch (e) {
			error = e;
		} finally {
			creating = false;
		}
	}

	async function revoke() {
		if (!created) return;
		try {
			await unwrapEmpty(
				api.DELETE('/api/v1/agent-enrollments/{enrollmentId}', {
					params: { path: { enrollmentId: created.enrollment.id } }
				})
			);
			await qc.invalidateQueries({ queryKey: liveKeys.list('agents') });
			toast.success('Revoked enrollment token');
			releaseCritical?.();
			void goto(routes.environments());
		} catch (e) {
			toast.error('The token could not be revoked.', { body: errorMessage(e) });
		}
	}
</script>

{#if perms.data && !canEnroll}
	<DeniedState
		level={1}
		title="You can't add environments."
		description="Enrolling agents needs the agent.enroll permission. Ask the owner of this Docker Manager."
	/>
{:else}
	<div class="page">
		<PageHeader
			{title}
			description={reattachId
				? 'Run a Docker Agent on the same Docker Engine. Once it enrolls, the environment comes back with its stacks and policies.'
				: 'Run the Docker Agent on a Docker host and enroll it with a one-time token. The agent dials out to this Docker Manager; the host opens no ports.'}
		/>

		{#if !created}
			<Card title={reattachId ? 'Enrollment token for re-attaching' : 'Enrollment token'}>
				{#if reattachId && reattach.isError}
					<ErrorState
						error={reattach.error}
						title="The environment could not be loaded."
						compact
					/>
				{:else}
					<form class="form" onsubmit={create}>
						{#if reattachId}
							<p>
								Re-attaches <strong>{reattach.data?.name ?? '…'}</strong>{reattach
									.data?.archivedAt
									? `, archived ${formatRelative(reattach.data.archivedAt)}`
									: ''}. The agent must run on the Engine this environment used; a
								different Engine is refused.
							</p>
						{:else}
							<TextField
								label="Environment name"
								bind:value={name}
								description="Optional. Otherwise the agent's DOCKER_AGENT_ENVIRONMENT_NAME or the Engine host name. You can rename it later."
								maxlength={64}
								autocomplete="off"
								error={fieldError(error, 'body.environmentName')}
							/>
						{/if}
						<Select
							label="Token expires after"
							options={TOKEN_LIFETIMES}
							bind:value={lifetime}
							description="The token works once. Unused tokens stop working when they expire."
						/>
						{#if !reattachId}
							<Checkbox
								bind:checked={duplicate}
								label="This host is a clone of an enrolled host"
								description="Only for cloned virtual machines that report the same Docker Engine ID as another environment."
							/>
						{/if}
						{#if error && !fieldError(error, 'body.environmentName')}
							<Notice
								tone="danger"
								title="The token could not be created."
								live="alert"
							>
								{errorMessage(error)}
							</Notice>
						{/if}
						<div class="actions">
							<Button variant="primary" type="submit" icon={Plus} loading={creating}
								>Create enrollment token</Button
							>
							<Button variant="ghost" href={routes.environments()}>Cancel</Button>
						</div>
					</form>
				{/if}
			</Card>
		{:else}
			<Card title="Enrollment" id="enrollment-status">
				<div class="status" role="status" aria-live="polite">
					{#if current?.state === 'pending'}
						<Spinner size={16} />
						<div>
							<p class="strong">Waiting for the agent to connect.</p>
							<p class="muted">
								Run one of the commands below. The token expires {formatRelative(
									current.expiresAt
								)} ({formatDateTime(current.expiresAt)}).
							</p>
							{#if current.lastRejection}
								<Notice tone="danger" title="The agent was refused." live="alert">
									{REJECTIONS[current.lastRejection.code] ??
										current.lastRejection.message}
									{#if current.lastRejection.hostname}(Host {current.lastRejection
											.hostname}.){/if}
									The token stays usable until it expires.
								</Notice>
							{/if}
						</div>
					{:else if current?.state === 'used'}
						<StatusBadge status="succeeded" label="Enrolled" />
						<div>
							<p class="strong">
								{enrolledEnv
									? `${enrolledEnv.name} is enrolled.`
									: 'The agent is enrolled.'}
							</p>
							<p class="muted">
								The environment shows as online once its first inventory arrives.
							</p>
						</div>
					{:else if current}
						<StatusBadge status={current.state} />
						<p class="muted">
							{current.state === 'expired'
								? 'The token expired before an agent used it. Create a new one.'
								: 'The token was revoked. Create a new one to enroll an agent.'}
						</p>
					{/if}
				</div>
				<div class="actions">
					{#if current?.state === 'used' && enrolledEnv}
						<Button variant="primary" href={routes.environment(enrolledEnv.id)}
							>Open {enrolledEnv.name}</Button
						>
					{/if}
					{#if current?.state === 'pending'}
						<Button variant="danger-soft" onclick={revoke}>Revoke token</Button>
					{/if}
					<Button variant="ghost" href={routes.environments()}
						>Back to environments</Button
					>
				</div>
			</Card>

			{#if showSecrets && current?.state === 'pending'}
				<Card title="Run the agent" subtitle="The commands contain the token">
					<Tabs items={commandTabs} bind:value={variant} label="Install commands">
						{#snippet panel(id)}
							{@const c = commands.find((x) => x.variant === id)}
							{#if c}<InstallCommand
									title={c.title}
									description={c.description}
									command={c.command}
								/>{/if}
						{/snippet}
					</Tabs>
					<p class="muted note">
						Agents dial out to <span class="mono">{created.managerUrl}</span>. Docker
						socket access gives the agent host-level authority.
					</p>
				</Card>
				<Card title="Enrollment token">
					<SecretReveal
						secret={created.token}
						label="enrollment token"
						filename="docker-manager-enrollment-token.txt"
						description="For a manual `docker-agent enroll`. The commands above already contain it. It works once."
						confirmLabel="Hide token and commands"
						onconfirm={() => (showSecrets = false)}
					/>
				</Card>
			{/if}
		{/if}
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		max-width: 960px;
	}

	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		max-width: 560px;
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.status {
		display: flex;
		align-items: flex-start;
		gap: var(--space-3);
		margin-bottom: var(--space-4);
	}

	.status > div {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}

	.strong {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.note {
		margin-top: var(--space-4);
	}
</style>
