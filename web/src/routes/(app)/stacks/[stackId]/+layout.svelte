<script lang="ts">
	// Stack detail (#22 mockup, #7): header with the stack's actions, the
	// state notices (offline environment, failed deploy), the route tabs
	// (Overview · Files · Logs · Terminal · Revisions · Backups · Policies ·
	// Activity)
	// with the "Undeployed changes" chip, the jobs started here, then the
	// tab. Files, Logs and Terminal are track B3's routes.
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { untrack } from 'svelte';
	import Layers from '@lucide/svelte/icons/layers';
	import { ApiRequestError } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { provideStackPage } from '$lib/features/stacks/context';
	import JobTrayView from '$lib/features/stacks/JobTrayView.svelte';
	import { RemoveOrphansRequest } from '$lib/features/stacks/deploy.svelte';
	import { canAnywhere, shortHash, stackTitle } from '$lib/features/stacks/model';
	import { stackQuery } from '$lib/features/stacks/queries';
	import StackHeader from '$lib/features/stacks/StackHeader.svelte';
	import { JobTray } from '$lib/features/stacks/tray.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		EmptyState,
		ErrorState,
		Notice,
		OfflineEnvironment,
		Skeleton,
		TabNav,
		type TabLink
	} from '$lib/ui';

	let { children } = $props();

	const id = $derived(page.params.stackId ?? '');
	const stack = createQuery(() => stackQuery(id));
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const s = $derived(stack.data);
	const environment = $derived(envs.data?.find((e) => e.id === s?.environmentId));
	const title = $derived(s ? stackTitle(s) : 'Stack');
	const tray = new JobTray();
	const removeOrphans = new RemoveOrphansRequest();
	// The layout stays mounted when another stack opens: start its tray empty.
	let trayFor = '';
	$effect(() => {
		if (id === trayFor) return;
		trayFor = id;
		untrack(() => {
			tray.jobs = [];
			tray.finished = [];
		});
	});

	provideStackPage({
		get id() {
			return id;
		},
		get stack() {
			return s;
		},
		get environment() {
			return environment;
		},
		tray,
		removeOrphans
	});

	// The migration wizard is a page of its own, without the tabs.
	const wizard = $derived(page.url.pathname.endsWith('/migrate'));
	usePage(() => ({
		title: wizard ? `Migrate ${title}` : title,
		crumbs: wizard
			? [
					{ label: 'Stacks', href: routes.stacks() },
					{ label: title, href: routes.stack(id) },
					{ label: 'Migrate' }
				]
			: [{ label: 'Stacks', href: routes.stacks() }, { label: title }],
		environmentScoped: true
	}));

	const notFound = $derived(
		stack.error instanceof ApiRequestError &&
			(stack.error.status === 404 || stack.error.status === 403)
	);
	const can = (a: string) => !!s?.actions.includes(a);
	const tabs = $derived.by((): TabLink[] => {
		if (!s) return [];
		const t: TabLink[] = [{ href: routes.stack(id), label: 'Overview' }];
		if (can('stack.files.read') || can('stack.definition.read'))
			t.push({ href: routes.stack(id, 'files'), label: 'Files' });
		if (can('container.logs.read')) t.push({ href: routes.stack(id, 'logs'), label: 'Logs' });
		if (can('container.exec'))
			t.push({ href: routes.stack(id, 'terminal'), label: 'Terminal' });
		if (can('stack.definition.read'))
			t.push({ href: routes.stack(id, 'revisions'), label: 'Revisions' });
		if (canAnywhere(perms.data, 'backup.read'))
			t.push({ href: routes.stack(id, 'backups'), label: 'Backups' });
		if (s.view === 'full') t.push({ href: routes.stack(id, 'policies'), label: 'Policies' });
		t.push({ href: routes.stack(id, 'activity'), label: 'Activity' });
		return t;
	});
	const offline = $derived(!!s && (s.readOnly || s.environmentOnline === false));

	// A job started on the way here (Create and deploy, a migration): show
	// it in the tray, then drop it from the URL.
	$effect(() => {
		const job = page.url.searchParams.get('job');
		if (!s || !job) return;
		const kind = page.url.searchParams.get('kind') ?? 'deploy';
		const t = stackTitle(s);
		tray.add(
			{ id: job },
			kind === 'migrate'
				? {
						title: `Migrate ${t}`,
						success: `Migrated ${t}`,
						failure: `${t} was not migrated`
					}
				: {
						title: `Deploy ${t}`,
						success: `Deployed ${t}`,
						failure: `${t} was not deployed`
					}
		);
		const url = new URL(page.url);
		url.searchParams.delete('job');
		url.searchParams.delete('kind');
		void goto(url.pathname + url.search, {
			replaceState: true,
			noScroll: true,
			keepFocus: true
		});
	});
</script>

{#if stack.isPending}
	<div class="page" aria-busy="true" aria-label="Loading the stack">
		<div class="head-skeleton">
			<Skeleton width="48px" height="48px" radius="md" />
			<div class="grow"><Skeleton lines={3} /></div>
		</div>
		<Skeleton height="36px" />
		<div class="kpis">
			{#each [0, 1, 2, 3, 4, 5] as i (i)}<Skeleton height="92px" radius="lg" />{/each}
		</div>
		<Skeleton height="280px" radius="lg" />
	</div>
{:else if notFound}
	<EmptyState
		icon={Layers}
		color="blue"
		title="This stack does not exist or you can't see it."
		description="It may have been deleted or moved, or your access changed. Stacks you can see are listed under Stacks."
		level={1}
	>
		{#snippet actions()}<Button variant="primary" href={routes.stacks()}>Open stacks</Button
			>{/snippet}
	</EmptyState>
{:else if stack.isError}
	<ErrorState
		error={stack.error}
		title="The stack could not be loaded."
		onretry={() => stack.refetch()}
	/>
{:else if s}
	<div class="page">
		<StackHeader stack={s} {environment} {tray} {removeOrphans} />

		{#if offline}
			<OfflineEnvironment
				name={environment?.name ?? 'The environment'}
				since={environment?.connectionChangedAt}
			/>
		{/if}
		{#if s.status === 'failed'}
			<Notice tone="danger" title="The last deploy of {title} failed.">
				{s.recovery ?? 'Fix the definition and deploy again.'}
				{#if s.failedRevision}
					The failed deploy used revision {s.failedRevision.seq} ({shortHash(
						s.failedRevision.hash
					)}).
				{/if}
				{#snippet actions()}
					{#if can('stack.definition.read')}
						<Button size="sm" href={routes.stack(id, 'revisions')}
							>Open revisions</Button
						>
					{/if}
					{#if s.lastJob}
						<Button size="sm" href={routes.job(s.lastJob.id)}>Open the job</Button>
					{/if}
				{/snippet}
			</Notice>
		{/if}

		{#if !wizard}
			<TabNav label="{title} sections" current={page.url.pathname} items={tabs}>
				{#snippet after()}
					{#if s.undeployedChanges}
						<a class="chip" href={routes.stack(id, 'revisions')}
							><Badge
								tone="warn"
								dot
								title="The files on disk differ from the deployed revision"
								>Undeployed changes</Badge
							></a
						>
					{/if}
				{/snippet}
			</TabNav>
		{/if}

		<JobTrayView {tray} />

		{#key id}{@render children()}{/key}
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
	}

	.head-skeleton {
		display: flex;
		gap: var(--space-4);
	}

	.grow {
		flex: 1;
		max-width: 420px;
	}

	.kpis {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
		gap: var(--space-4);
	}

	.chip {
		display: inline-flex;
		border-radius: var(--radius-sm);
		text-decoration: none;
	}
</style>
