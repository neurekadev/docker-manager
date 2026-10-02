<script lang="ts">
	// Add environment (#3) and re-attach an archived one (#34) in three
	// steps: 1 name it (token lifetime and the clone option under "More
	// Options"), 2 run the generated command (it carries a one-use
	// enrollment token from POST /agent-enrollments, intent new or
	// reattach:<id>) with the waiting status under it, 3 connected. The
	// token lives only in this page's memory; while it is shown, the PWA
	// update prompt will not reload the page (criticalWork).
	import { onDestroy } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Plus from '@lucide/svelte/icons/plus';
	import { api, unwrap, unwrapEmpty, type Schema } from '$lib/api/client';
	import {
		enrollmentsQuery,
		environmentQuery,
		environmentsQuery,
		myPermissionsQuery
	} from '$lib/api/queries';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import Page from '$lib/features/common/Page.svelte';
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
	const title = $derived(reattachId ? 'Re-Attach Environment' : 'Add Environment');

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
	const pending = $derived(current?.state === 'pending');
	const connected = $derived(current?.state === 'used');

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
			toast.success('Created the install command', {
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
			toast.success('Revoked the install command');
			releaseCritical?.();
			void goto(routes.environments());
		} catch (e) {
			toast.error('The install command could not be revoked.', { body: errorMessage(e) });
		}
	}
</script>

{#snippet stepHead(n: number, text: string, done: boolean)}
	<header class="step-head">
		<span class="step-num" class:done aria-hidden="true"
			>{#if done}<CircleCheck size={16} strokeWidth={2} />{:else}{n}{/if}</span
		>
		<h2 id="step-{n}">{text}</h2>
		{#if done}<span class="sr-only">(done)</span>{/if}
	</header>
{/snippet}

{#if perms.data && !canEnroll}
	<DeniedState
		level={1}
		title="You can't add environments."
		description="Ask the owner of this Docker Manager for access."
	/>
{:else}
	<Page narrow>
		<PageHeader
			{title}
			description={reattachId
				? 'Run the Docker Agent on the same host again. Once it connects, the environment comes back with its stacks and policies.'
				: 'Connect a Docker host by running the Docker Agent on it.'}
		/>

		{#if reattachId && reattach.isError}
			<ErrorState
				error={reattach.error}
				title="The environment could not be loaded."
				onretry={() => reattach.refetch()}
			/>
		{:else}
			<ol class="steps" role="list">
				<li>
					<Card>
						<section class="step" aria-labelledby="step-1">
							{@render stepHead(1, reattachId ? 'Environment' : 'Name', !!created)}
							{#if created}
								<p>
									{#if reattachId}
										Re-attaches <strong>{reattach.data?.name ?? '…'}</strong>.
									{:else if name.trim()}
										<strong>{name.trim()}</strong>
									{:else}
										<span class="muted">The host's name.</span>
									{/if}
								</p>
							{:else}
								<form class="form" onsubmit={create}>
									{#if reattachId}
										<p>
											Re-attaches <strong>{reattach.data?.name ?? '…'}</strong
											>{reattach.data?.archivedAt
												? `, archived ${formatRelative(reattach.data.archivedAt)}`
												: ''}. Run the agent on the same host it used
											before; another host is refused.
										</p>
									{:else}
										<div class="field">
											<TextField
												label="Environment Name"
												bind:value={name}
												description="Optional. Without one, the host's name is used. You can rename it later."
												maxlength={64}
												autocomplete="off"
												error={fieldError(error, 'body.environmentName')}
											/>
										</div>
									{/if}
									<Disclosure summary="More Options">
										<div class="field">
											<Select
												label="Command Expires After"
												options={TOKEN_LIFETIMES}
												bind:value={lifetime}
												description="The command works once. An unused one stops working when it expires."
											/>
										</div>
										{#if !reattachId}
											<Checkbox
												bind:checked={duplicate}
												label="This host is a clone of a connected host"
												description="Only for a cloned virtual machine that Docker Manager would otherwise mistake for another environment."
											/>
										{/if}
									</Disclosure>
									{#if error && !fieldError(error, 'body.environmentName')}
										<Notice
											tone="danger"
											title="The install command could not be created."
											live="alert"
										>
											{errorMessage(error)}
										</Notice>
									{/if}
									<div class="actions">
										<Button
											variant="primary"
											type="submit"
											icon={Plus}
											loading={creating}>Create Install Command</Button
										>
										<Button variant="ghost" href={routes.environments()}
											>Cancel</Button
										>
									</div>
								</form>
							{/if}
						</section>
					</Card>
				</li>

				<li>
					<Card>
						<section class="step" aria-labelledby="step-2">
							{@render stepHead(2, 'Run This Command', connected)}
							{#if !created}
								<p class="muted">The command appears here once you create it.</p>
							{:else}
								{#if showSecrets && pending}
									<Tabs
										items={commandTabs}
										bind:value={variant}
										label="Install Commands"
									>
										{#snippet panel(id)}
											{@const c = commands.find((x) => x.variant === id)}
											{#if c}<InstallCommand
													title={c.title}
													description={INSTALL_VARIANTS[c.variant]
														?.description ?? c.description}
													command={c.command}
												/>{/if}
										{/snippet}
									</Tabs>
								{:else if pending}
									<p class="muted">
										The command is hidden. Revoke it and create a new one if you
										need it again.
									</p>
								{/if}

								<div class="status" role="status" aria-live="polite">
									{#if pending && current}
										<Spinner size={16} />
										<p>
											Waiting for the agent to connect. The command works once
											and expires <time
												datetime={current.expiresAt}
												title={formatDateTime(current.expiresAt)}
												>{formatRelative(current.expiresAt)}</time
											>.
										</p>
									{:else if connected}
										<StatusBadge status="succeeded" label="Connected" />
									{:else if current}
										<StatusBadge status={current.state} />
										<p class="muted">
											{current.state === 'expired'
												? 'The command expired before an agent used it. Create a new one.'
												: 'The command was revoked. Create a new one to connect a host.'}
										</p>
									{/if}
								</div>
								{#if pending && current?.lastRejection}
									<Notice
										tone="danger"
										title="The agent was refused."
										live="alert"
									>
										{REJECTIONS[current.lastRejection.code] ??
											current.lastRejection.message}
										{#if current.lastRejection.hostname}(Host {current
												.lastRejection.hostname}.){/if}
										The command keeps working until it expires.
									</Notice>
								{/if}

								{#if showSecrets && pending}
									<p class="muted note">
										The agent connects out to <span class="mono"
											>{created.managerUrl}</span
										>; the host needs no open ports.
									</p>
									<Disclosure summary="Connect by Hand with the Token">
										<SecretReveal
											secret={created.token}
											label="enrollment token"
											filename="docker-manager-enrollment-token.txt"
											description="For setting up the agent yourself. The commands above already contain it. It works once."
											confirmLabel="Hide Token and Commands"
											onconfirm={() => (showSecrets = false)}
										/>
									</Disclosure>
								{/if}
								{#if pending}
									<div class="actions">
										<Button variant="danger-soft" onclick={revoke}
											>Revoke Command</Button
										>
									</div>
								{/if}
							{/if}
						</section>
					</Card>
				</li>

				<li>
					<Card>
						<section class="step" aria-labelledby="step-3">
							{@render stepHead(3, 'Connected', connected)}
							{#if connected}
								<p class="strong">
									{enrolledEnv
										? `${enrolledEnv.name} is connected.`
										: 'The host is connected.'}
								</p>
								<p class="muted">
									It shows as online once its first inventory arrives.
								</p>
								<div class="actions">
									{#if enrolledEnv}
										<Button
											variant="primary"
											href={routes.environment(enrolledEnv.id)}
											>Open {enrolledEnv.name}</Button
										>
									{/if}
									<Button variant="ghost" href={routes.environments()}
										>Back to Environments</Button
									>
								</div>
							{:else}
								<p class="muted">
									The host appears here as soon as its agent connects.
								</p>
							{/if}
						</section>
					</Card>
				</li>
			</ol>
		{/if}
	</Page>
{/if}

<style>
	.steps {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.step {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
	}

	.step-head {
		display: flex;
		align-items: center;
		gap: var(--space-3);
	}

	.step-num {
		display: inline-grid;
		flex-shrink: 0;
		place-items: center;
		width: 28px;
		height: 28px;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-full);
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-semibold);
	}

	.step-num.done {
		border-color: var(--ok-border);
		background: var(--ok-soft);
		color: var(--ok);
	}

	h2 {
		font-size: var(--text-section);
		line-height: var(--leading-section);
	}

	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	/* A single field reads best at a form's width, not the page's. */
	.field {
		max-width: 480px;
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.status {
		display: flex;
		align-items: center;
		gap: var(--space-3);
	}

	.strong {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}
</style>
