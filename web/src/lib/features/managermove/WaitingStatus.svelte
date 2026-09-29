<script lang="ts">
	// The new server's status page (public, read-only; docs/internal/architecture/manager-move.md,
	// "Waiting mode and status page"). A manager in waiting mode answers
	// every route but GET /api/v1/move/status with 503, so this page is
	// built from that route alone (no session, no cookies: it works over
	// plain http, and no live stream). Polls every 3 s; while the manager restarts the last
	// answer stays with a note. The steps, with the current one
	// highlighted, and one notice: done (point DNS here), a problem with
	// its recovery, or what to do next.
	import { createQuery } from '@tanstack/svelte-query';
	import Circle from '@lucide/svelte/icons/circle';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleX from '@lucide/svelte/icons/circle-x';
	import AuthHeader from '$lib/features/auth/AuthHeader.svelte';
	import { routes } from '$lib/routes';
	import { Button, ErrorState, Notice, Skeleton, Spinner } from '$lib/ui';
	import { waitNotice, waitSteps } from './model';
	import { WAIT_POLL_MS, moveStatusQuery } from './queries';

	const status = createQuery(() => ({
		...moveStatusQuery(),
		refetchInterval: WAIT_POLL_MS,
		refetchIntervalInBackground: true
	}));
	const s = $derived(status.data);
	const steps = $derived(s ? waitSteps(s) : []);
	const notice = $derived(s ? waitNotice(s) : null);
	const STATE_WORDS = {
		done: 'done',
		current: 'in progress',
		todo: 'not started',
		failed: 'failed'
	};
</script>

<div class="status">
	<AuthHeader
		title="Moving Docker Manager here"
		lead={s?.oldManager
			? `From ${s.oldManager}. This page updates by itself.`
			: 'This page updates by itself.'}
	/>

	{#if !s}
		{#if status.isError}
			<ErrorState
				error={status.error}
				title="The move's progress could not be loaded."
				onretry={() => status.refetch()}
				retrying={status.isFetching}
				bare
				compact
			/>
		{:else}
			<div aria-busy="true"><Skeleton lines={5} /></div>
		{/if}
	{:else if s.phase === 'none'}
		<Notice tone="info" title="Docker Manager is not moving." live="none">
			Nothing to follow here.
		</Notice>
		<div><Button href={routes.dashboard()}>Open Docker Manager</Button></div>
	{:else}
		<ol class="steps" role="list" aria-label="Steps of the move">
			{#each steps as st (st.id)}
				<li
					class={st.state}
					aria-current={st.state === 'current' || st.state === 'failed'
						? 'step'
						: undefined}
				>
					<span class="mark" aria-hidden="true">
						{#if st.state === 'done'}<CircleCheck size={18} strokeWidth={1.75} />
						{:else if st.state === 'failed'}<CircleX size={18} strokeWidth={1.75} />
						{:else if st.state === 'current'}<Spinner size={16} />
						{:else}<Circle size={18} strokeWidth={1.75} />{/if}
					</span>
					<span>{st.label}<span class="sr-only">: {STATE_WORDS[st.state]}</span></span>
				</li>
			{/each}
		</ol>
		{#if notice}
			<Notice
				tone={notice.tone}
				title={notice.title}
				live={notice.tone === 'danger' ? 'alert' : 'status'}>{notice.body}</Notice
			>
		{/if}
		{#if status.isError}
			<p class="muted" role="status">
				Docker Manager on this server does not answer right now. Trying again…
			</p>
		{/if}
	{/if}
</div>

<svelte:head><title>Moving Docker Manager · Docker Manager</title></svelte:head>

<style>
	.status {
		display: grid;
		gap: var(--space-5);
	}

	.steps {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.steps li {
		display: flex;
		align-items: flex-start;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		color: var(--text-muted);
	}

	.mark {
		display: grid;
		flex: none;
		place-items: center;
		width: 18px;
		height: 20px;
	}

	.steps li.done {
		color: var(--text-default);
	}

	.steps li.done .mark {
		color: var(--ok);
	}

	.steps li.current {
		border-color: var(--border-subtle);
		background: var(--surface-raised);
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.steps li.current .mark {
		color: var(--accent-text);
	}

	.steps li.failed {
		border-color: var(--border-subtle);
		background: var(--surface-raised);
		color: var(--text-strong);
	}

	.steps li.failed .mark {
		color: var(--danger);
	}
</style>
