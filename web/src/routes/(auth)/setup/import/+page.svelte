<script lang="ts">
	// Import from backup (#10, #24): a new, empty Docker Manager recovers the
	// manager from its backups before any owner exists. Destination (local
	// or S3 with newly issued keys) and the Recovery Key → connection test
	// (which repositories open, with which key) → the backup sets found in
	// the portable manifests with their completeness and compatibility →
	// import (the manager restarts to apply it) → re-attach the hosts. The
	// key and credentials stay in this page only; nothing is stored here.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import { api, unwrap } from '$lib/api/client';
	import { healthQuery, queryKeys, setupStatusQuery } from '$lib/api/queries';
	import { useCriticalWork } from '$lib/features/common/unsaved.svelte';
	import { routes } from '$lib/routes';
	import AuthHeader from '$lib/features/auth/AuthHeader.svelte';
	import {
		Badge,
		Checkbox,
		Notice,
		RadioGroup,
		Skeleton,
		StepWizard,
		TextArea,
		formatDateTime
	} from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import DestinationFields from '$lib/features/backups/DestinationFields.svelte';
	import { destinationReady, emptyDestination } from '$lib/features/backups/destination';
	import {
		IMPORT_ERRORS,
		bundleText,
		importBlocker,
		importSource,
		located,
		locationState,
		type ImportPreview,
		type ImportTest
	} from '$lib/features/backups/importModel';
	import {
		KIND_LABEL,
		RECOVERY_KEY_WARNING,
		itemName,
		looksLikeRecoveryKey,
		setState
	} from '$lib/features/backups/model';

	const qc = useQueryClient();
	let started = $state(false);
	const status = createQuery(() => ({
		...setupStatusQuery(),
		staleTime: 0,
		refetchInterval: started ? 2000 : false,
		retry: true
	}));
	const health = createQuery(() => ({
		...healthQuery(),
		refetchInterval: started ? 3000 : false
	}));
	const secure = $derived(status.data?.secureOrigin ?? true);

	// Setup already done (and not by this import): sign in instead.
	$effect(() => {
		if (!started && status.data?.setupComplete)
			void goto(routes.signIn(), { replaceState: true });
	});

	let current = $state(0);
	let dest = $state(emptyDestination());
	let recoveryKey = $state('');
	let previousKey = $state('');
	let test = $state<ImportTest | null>(null);
	let preview = $state<ImportPreview | null>(null);
	let setId = $state('');
	let checked = $state<ImportPreview | null>(null);
	let confirm = $state(false);

	const job = $derived(status.data?.backupImport);
	const done = $derived(started && !!status.data?.setupComplete);
	const failed = $derived(
		started && job && ['failed', 'cancelled', 'interrupted'].includes(job.state)
	);

	// Secrets typed here must not be reloaded away mid-import (#23).
	useCriticalWork(
		'restore',
		() => 'Backup import',
		() => !!recoveryKey && !done
	);

	function source(extra = {}) {
		return importSource(dest, recoveryKey, previousKey, extra);
	}

	function explain(e: unknown): Error {
		return new Error(actionError(e, IMPORT_ERRORS), { cause: e });
	}

	const steps = [
		{
			id: 'source',
			label: 'Backups',
			description: 'Where the backups are and the Recovery Key that opens them.'
		},
		{
			id: 'test',
			label: 'Check Access',
			description: 'Which repositories this Docker Manager can open.'
		},
		{
			id: 'set',
			label: 'Choose a Backup',
			description: 'Backup sets found in the repositories, newest first.'
		},
		{
			id: 'import',
			label: 'Import',
			description: 'Restore the manager state and restart.'
		}
	];

	async function onnext(step: { id: string }) {
		try {
			if (step.id === 'source') {
				test = await unwrap(
					api.POST('/api/v1/setup/backup-imports/connection-tests', { body: source() })
				);
				preview = null;
				checked = null;
				setId = '';
			}
			if (step.id === 'test') {
				if (!test?.ok) return false;
				preview = await unwrap(
					api.POST('/api/v1/setup/backup-imports/previews', { body: source() })
				);
				const first = preview.sets.find((s) => !importBlocker(s));
				setId = first?.setId ?? '';
				checked = null;
			}
			if (step.id === 'set') {
				checked = await unwrap(
					api.POST('/api/v1/setup/backup-imports/previews', { body: source({ setId }) })
				);
				const s = checked.sets.find((x) => x.setId === setId);
				if (s && s.keyBundle && s.keyBundle !== 'ok') return false;
			}
			if (step.id === 'import') {
				if (!started) {
					await unwrap(
						api.POST('/api/v1/setup/backup-imports/restores', {
							body: source({ setId, confirm: true })
						})
					);
					started = true;
					return false;
				}
				if (!done) return false;
			}
		} catch (e) {
			throw explain(e);
		}
	}

	async function finish() {
		recoveryKey = '';
		previousKey = '';
		dest.secretAccessKey = '';
		qc.removeQueries();
		await qc.invalidateQueries({ queryKey: queryKeys.setupStatus });
		await goto(routes.signIn(), { replaceState: true });
	}

	const selectedSet = $derived((checked ?? preview)?.sets.find((s) => s.setId === setId) ?? null);
	const canAdvance = $derived(
		current === 0
			? secure && destinationReady(dest) && looksLikeRecoveryKey(recoveryKey)
			: current === 1
				? !!test?.ok
				: current === 2
					? !!setId && !!selectedSet && !importBlocker(selectedSet)
					: started
						? done
						: confirm
	);

	function setLabel(s: NonNullable<typeof preview>['sets'][number]): string {
		const when = s.createdAt ? formatDateTime(s.createdAt) : 'Unknown time';
		return `${when}${s.policyName ? `, ${s.policyName}` : ''}`;
	}
</script>

<svelte:head><title>Import From Backup · Docker Manager</title></svelte:head>

<div class="stack">
	<AuthHeader
		title="Import From Backup"
		lead="Recover a Docker Manager from its backups on this new, empty manager. You need the backup location, its access keys if it is S3, and your Recovery Key; not the old manager."
	>
		{#snippet before()}
			<a class="back" href={routes.setup()}
				><ArrowLeft size={14} aria-hidden="true" /> Set Up a New Docker Manager Instead</a
			>
		{/snippet}
	</AuthHeader>

	{#if status.isPending}
		<Skeleton lines={5} height="36px" />
	{:else}
		{#if !secure}
			<Notice
				tone="danger"
				icon={ShieldAlert}
				title="Import on Docker Manager's Public URL"
				live="alert"
			>
				{status.data?.explanation ??
					"This page was not opened over HTTPS on Docker Manager's public address."}
			</Notice>
		{/if}
		<StepWizard
			label="Import From Backup"
			{steps}
			bind:current
			{onnext}
			onfinish={finish}
			{canAdvance}
			nextLabel={current === 0 ? 'Check Access' : current === 1 ? 'Show Backups' : 'Next'}
			finishLabel={done ? 'Sign In' : started ? 'Importing…' : 'Import and Restart'}
		>
			{#snippet step(s)}
				{#if s.id === 'source'}
					<Fields>
						<DestinationFields
							bind:value={dest}
							localDescription="The folder with the backups, as mounted into this Docker Manager's container in one of its backup folders. It may be a new path."
						/>
						<TextArea
							label="Recovery Key"
							mono
							rows={2}
							bind:value={recoveryKey}
							autocomplete="off"
							spellcheck="false"
							placeholder="DYRK-XXXX-XXXX-…"
							description="The key you saved when backups were set up (the newest one after a rotation). Never stored or logged."
						/>
						<Disclosure summary="The key was rotated recently">
							<TextArea
								label="Previous Recovery Key"
								mono
								rows={2}
								bind:value={previousKey}
								autocomplete="off"
								spellcheck="false"
								description="Optional. Needed for repositories the rotation had not reached yet, or for sets saved before it."
							/>
						</Disclosure>
						<p class="muted small">{RECOVERY_KEY_WARNING}</p>
					</Fields>
				{:else if s.id === 'test' && test}
					<Fields>
						<div class="row">
							{#if test.ok}<Badge tone="ok" dot>Access Works</Badge>{:else}<Badge
									tone="danger"
									dot>No Access</Badge
								>{/if}
							<span class="muted"
								>Key Fingerprint <span class="mono">{test.keyFingerprint}</span
								></span
							>
							<span class="muted"
								>{test.sets} backup {test.sets === 1 ? 'set' : 'sets'} found</span
							>
						</div>
						{#each test.problems as p (p)}<Notice
								tone="warn"
								title="Needs Attention"
								live="none">{p}</Notice
							>{/each}
						{#if test.objectLock}
							<Notice tone="info" title="Object Lock is on" live="none"
								>Importing works; retention may be unable to delete old backups
								later.</Notice
							>
						{/if}
						<ul class="locs" role="list">
							{#each [test.manager, ...test.locations] as l (l.scope)}
								{@const st = locationState(l)}
								<li>
									<span class="name"
										>{l.scope === 'manager'
											? 'Manager'
											: (l.environmentName ?? l.scope)}</span
									>
									<Badge tone={st.tone} dot>{st.label}</Badge>
									<span class="mono muted small">{l.repository}</span>
									{#if l.note}<span class="muted small">{l.note}</span>{/if}
								</li>
							{/each}
						</ul>
					</Fields>
				{:else if s.id === 'set' && preview}
					<Fields>
						{#if preview.sets.length === 0}
							<Notice tone="warn" title="No Backup Sets Found" live="none">
								The repositories hold no Docker Manager backup set. Check the
								destination and the key.
							</Notice>
						{:else}
							<RadioGroup
								label="Backup Set"
								bind:value={setId}
								onchange={() => (checked = null)}
								options={preview.sets.map((x) => ({
									value: x.setId,
									label: setLabel(x),
									description:
										importBlocker(x) ??
										`${x.completeness ? setState(x.completeness).label : 'Unknown'} set${x.appVersion ? `, Docker Manager ${x.appVersion}` : ''}`,
									disabled: !!importBlocker(x)
								}))}
							/>
						{/if}
						{#if selectedSet}
							<div class="members">
								<p class="head">What This Set Holds</p>
								<ul role="list">
									{#each selectedSet.members as m, i (`${m.scope}-${m.item}-${i}`)}
										{@const l = located(m.located)}
										<li>
											<span class="name">{itemName(m)}</span>
											<span class="muted small"
												>{m.kind === 'manager_state'
													? 'Docker Manager itself'
													: KIND_LABEL[m.kind]}{m.environmentName
													? `, ${m.environmentName}`
													: ''}</span
											>
											<Badge tone={l.tone} dot>{l.label}</Badge>
											{#if m.snapshotTime}<span class="muted small num"
													>{formatDateTime(m.snapshotTime)}</span
												>{/if}
										</li>
									{/each}
								</ul>
								{#each selectedSet.problems as p (p)}<p class="warn small">
										{p}
									</p>{/each}
								{#if selectedSet.keyBundle}
									<Notice
										tone={selectedSet.keyBundle === 'ok' ? 'info' : 'danger'}
										title={selectedSet.keyBundle === 'ok'
											? 'Ready to Import'
											: 'This set cannot be imported yet'}
										live="status"
									>
										{bundleText(selectedSet.keyBundle)}
									</Notice>
								{/if}
								<p class="muted small">
									Stacks and volumes on hosts are restored later from their own
									backups; “not reachable yet” means the host repository becomes
									usable once that host is re-attached.
								</p>
							</div>
						{/if}
					</Fields>
				{:else if s.id === 'import'}
					<Fields>
						{#if !started}
							<Notice tone="warn" title="What the Import Does" live="none">
								<ul class="plain" role="list">
									<li>
										This manager's empty state is replaced by the backup and the
										manager restarts.
									</li>
									<li>
										You sign in with the owner account from the backup. No old
										session is revived.
									</li>
									<li>
										Every API token from the backup is revoked; create new ones.
									</li>
									<li>
										Every environment waits to be re-attached: enroll its agent
										again (intent “reattach”).
									</li>
								</ul>
							</Notice>
							<Checkbox
								bind:checked={confirm}
								label="Replace this manager's state with the backup and restart"
							/>
						{:else if done}
							<Notice tone="info" title="Imported" live="status">
								Docker Manager restarted with the backup. Sign in with your owner
								account from the backup.
							</Notice>
							<ol class="plain steps" role="list">
								<li>
									Sign in and open Environments: every environment shows as
									waiting to be re-attached.
								</li>
								<li>
									On each host, enroll its agent again with an enrollment of
									intent “reattach” from the environment page. Its stacks,
									policies and backups come back with it.
								</li>
								<li>
									Mount host backup directories at the same paths as before, then
									restore stacks and volumes.
								</li>
								<li>Create new API tokens for your scripts.</li>
							</ol>
						{:else if failed}
							<Notice tone="danger" title="The import failed" live="alert">
								{job?.errorCode
									? (IMPORT_ERRORS[job.errorCode] ?? job.errorCode)
									: ''}
								{job?.recovery ??
									'Check the destination and the key, then start again.'}
							</Notice>
						{:else}
							<div aria-busy="true" class="progress">
								<Badge tone="info" dot pulse
									>{job?.restartPending || health.isError
										? 'Restarting Docker Manager'
										: job?.state === 'running'
											? 'Restoring the Manager State'
											: 'Queued'}</Badge
								>
								<p class="muted small">
									This page follows the import; keep it open.
								</p>
							</div>
						{/if}
					</Fields>
				{/if}
			{/snippet}
		</StepWizard>
	{/if}
</div>

<style>
	.stack {
		display: flex;
		flex-direction: column;
		gap: var(--space-5);
	}

	.back {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		margin-bottom: var(--space-2);
		font-size: var(--text-caption);
	}

	.row,
	.locs li,
	.members li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.locs,
	.members ul,
	.plain {
		display: grid;
		gap: var(--space-2);
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.members {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}

	.head {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.steps {
		padding-left: var(--space-5);
		list-style: decimal;
	}

	.small {
		font-size: var(--text-caption);
	}

	.warn {
		color: var(--warn);
	}

	.progress {
		display: grid;
		gap: var(--space-2);
		justify-items: start;
	}
</style>
