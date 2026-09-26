<script lang="ts">
	// The signed-in app shell (#22): sidebar (≥1280 px full, 1024–1279 px icon
	// rail, <1024 px off-canvas drawer), top bar (sidebar toggle, breadcrumbs,
	// live indicator, ⌘K search, notices, user menu), the offline banner
	// when the live stream has been down for 5 s, and the offline-environment
	// banner for the selected environment.
	import type { Snippet } from 'svelte';
	import { MediaQuery } from 'svelte/reactivity';
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import PanelLeft from '@lucide/svelte/icons/panel-left';
	import Search from '@lucide/svelte/icons/search';
	import CloudOff from '@lucide/svelte/icons/cloud-off';
	import type { Account } from '$lib/api/client';
	import {
		environmentsQuery,
		environmentSystemQuery,
		myPermissionsQuery,
		recentJobsQuery,
		updatePoliciesSummaryQuery
	} from '$lib/api/queries';
	import { jobKindLabel } from '$lib/features/jobs/labels';
	import { liveStatus } from '$lib/live/status.svelte';
	import { bannerDelay } from './live-banner';
	import { routes } from '$lib/routes';
	import Breadcrumbs from '$lib/ui/Breadcrumbs.svelte';
	import Drawer from '$lib/ui/Drawer.svelte';
	import IconButton from '$lib/ui/IconButton.svelte';
	import Kbd from '$lib/ui/Kbd.svelte';
	import Notice from '$lib/ui/Notice.svelte';
	import OfflineEnvironment from '$lib/ui/OfflineEnvironment.svelte';
	import CommandPalette from './CommandPalette.svelte';
	import EnvironmentSwitcher from './EnvironmentSwitcher.svelte';
	import LiveIndicator from './LiveIndicator.svelte';
	import NoticesBell from './NoticesBell.svelte';
	import Sidebar from './Sidebar.svelte';
	import UserMenu from './UserMenu.svelte';
	import { environmentSelection } from './environment.svelte';
	import { accessOf, activeNav, hasAny, isRestricted, visibleNav } from './nav';
	import { environmentNotices, jobNotices, updateNotices } from './notices.svelte';
	import { pageState } from './page.svelte';

	interface Props {
		user: Account;
		onsignout: () => void;
		children: Snippet;
	}

	let { user, onsignout, children }: Props = $props();

	const wide = new MediaQuery('min-width: 1280px');
	const medium = new MediaQuery('min-width: 1024px');
	const narrow = new MediaQuery('max-width: 767px');

	// UI preference only (not API data): the collapsed rail on wide screens.
	let collapsed = $state(
		typeof localStorage !== 'undefined' &&
			localStorage.getItem('docker-manager:sidebar') === 'rail'
	);
	let drawerOpen = $state(false);
	let paletteOpen = $state(false);

	const perms = createQuery(() => myPermissionsQuery());
	const envs = createQuery(() => environmentsQuery());
	const access = $derived(accessOf(perms.data));
	const nav = $derived(visibleNav(access));
	const active = $derived(activeNav(page.url.pathname));
	const environments = $derived(envs.data ?? []);

	$effect(() => {
		if (envs.data)
			environmentSelection.restore(
				user.id,
				envs.data.map((e) => e.id)
			);
	});

	const selected = $derived(environments.find((e) => e.id === environmentSelection.id) ?? null);
	const system = createQuery(() => ({
		...environmentSystemQuery(selected?.id ?? ''),
		enabled: !!selected?.online && !!selected?.actions.includes('environment.system.read')
	}));

	// Offline notices from successive environment lists.
	const feedNotices = environmentNotices();
	$effect(() => {
		if (envs.data) feedNotices(envs.data);
	});

	// Finished-job and update-available notices (#25 Q6): recent jobs and
	// update summaries, both refreshed by live events.
	const recentJobs = createQuery(() => ({
		...recentJobsQuery(20),
		enabled: !!perms.data && !isRestricted(access)
	}));
	const updatePolicies = createQuery(() => ({
		...updatePoliciesSummaryQuery(),
		enabled: hasAny(access, 'update_policy.')
	}));
	const feedJobNotices = jobNotices(() => user.id, jobKindLabel);
	const feedUpdateNotices = updateNotices();
	$effect(() => {
		if (recentJobs.data) feedJobNotices(recentJobs.data.items);
	});
	$effect(() => {
		if (updatePolicies.data) feedUpdateNotices(updatePolicies.data);
	});

	// The offline banner, once the live stream (#23) has been down for 5 s.
	let liveBanner = $state(false);
	$effect(() => {
		const delay = bannerDelay(liveStatus.state, liveStatus.since, Date.now());
		if (delay === null) {
			liveBanner = false;
			return;
		}
		const t = setTimeout(() => (liveBanner = true), delay);
		return () => clearTimeout(t);
	});

	const rail = $derived(medium.current && (!wide.current || collapsed));
	const drawerMode = $derived(!medium.current);

	const crumbs = $derived.by(() => {
		const meta = pageState.current;
		return meta.environmentScoped && selected
			? [{ label: selected.name, href: routes.environment(selected.id) }, ...meta.crumbs]
			: meta.crumbs;
	});

	function toggleSidebar() {
		if (drawerMode) {
			drawerOpen = true;
			return;
		}
		collapsed = !rail;
		try {
			localStorage.setItem('docker-manager:sidebar', collapsed ? 'rail' : 'full');
		} catch {
			// not persisted
		}
	}

	function onKeydown(e: KeyboardEvent) {
		if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
			e.preventDefault();
			paletteOpen = !paletteOpen;
		}
	}

	const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform);
</script>

<svelte:window onkeydown={onKeydown} />

{#snippet envSwitcher(compact: boolean)}
	{#if environments.length || hasAny(access, 'agent.enroll')}
		<EnvironmentSwitcher
			{environments}
			selected={environmentSelection.id}
			onselect={(id) => environmentSelection.select(id)}
			engineVersion={system.data?.engine?.version}
			canAdd={hasAny(access, 'agent.enroll')}
			{compact}
			loading={envs.isPending}
		/>
	{/if}
{/snippet}

<a class="skip" href="#main">Skip to content</a>

<div class="shell" class:rail class:drawer-mode={drawerMode}>
	{#if !drawerMode}
		<aside class="side">
			<Sidebar items={nav} activeId={active?.id} {rail}>
				{#snippet switcher()}{@render envSwitcher(rail)}{/snippet}
			</Sidebar>
		</aside>
	{/if}

	<div class="column">
		<header class="topbar">
			<IconButton
				label={drawerMode
					? 'Open navigation'
					: rail
						? 'Expand sidebar'
						: 'Collapse sidebar'}
				icon={PanelLeft}
				onclick={toggleSidebar}
				tooltipSide="bottom"
			/>
			<div class="crumbs">
				{#if crumbs.length}<Breadcrumbs items={crumbs} />{/if}
			</div>
			<LiveIndicator />
			{#if narrow.current}
				<IconButton
					label="Search"
					icon={Search}
					onclick={() => (paletteOpen = true)}
					tooltipSide="bottom"
				/>
			{:else}
				<button
					type="button"
					class="search"
					onclick={() => (paletteOpen = true)}
					aria-keyshortcuts={isMac ? 'Meta+K' : 'Control+K'}
				>
					<Search size={16} strokeWidth={1.75} aria-hidden="true" />
					<span class="placeholder">Search anything…</span>
					<Kbd>{isMac ? '⌘' : 'Ctrl'} K</Kbd>
				</button>
			{/if}
			<NoticesBell />
			<UserMenu {user} {onsignout} />
		</header>

		{#if liveBanner}
			<Notice tone="offline" icon={CloudOff} title="Live updates are disconnected" bar>
				Pages may be out of date. Docker Manager keeps trying to reconnect; nothing you do
				is queued.
			</Notice>
		{/if}
		{#if selected && !selected.online}
			<div class="env-offline">
				<OfflineEnvironment name={selected.name} since={selected.connectionChangedAt} />
			</div>
		{/if}

		<main id="main" tabindex="-1">
			{@render children()}
		</main>
	</div>
</div>

{#if drawerMode}
	<Drawer bind:open={drawerOpen} title="Navigation" hideTitle side="left" size="280px">
		<Sidebar items={nav} activeId={active?.id} onnavigate={() => (drawerOpen = false)}>
			{#snippet switcher()}{@render envSwitcher(false)}{/snippet}
		</Sidebar>
	</Drawer>
{/if}

<CommandPalette
	bind:open={paletteOpen}
	pages={nav}
	environmentId={environmentSelection.id}
	environmentName={selected?.name}
	onnavigate={(href) => goto(href)}
/>

<style>
	.skip {
		position: absolute;
		left: var(--space-2);
		top: -100px;
		z-index: var(--z-toast);
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-sm);
		background: var(--accent);
		color: var(--text-on-accent);
	}

	.skip:focus {
		top: var(--space-2);
	}

	.shell {
		display: grid;
		grid-template-columns: var(--sidebar-width) minmax(0, 1fr);
		min-height: 100dvh;
	}

	.shell.rail {
		grid-template-columns: var(--sidebar-rail) minmax(0, 1fr);
	}

	.shell.drawer-mode {
		grid-template-columns: minmax(0, 1fr);
	}

	.side {
		position: sticky;
		top: 0;
		height: 100dvh;
		border-right: 1px solid var(--border-subtle);
		background: var(--surface-shell);
	}

	.column {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.topbar {
		position: sticky;
		top: 0;
		z-index: var(--z-shell);
		display: flex;
		align-items: center;
		gap: var(--space-2);
		height: var(--topbar-height);
		padding: 0 var(--space-4) 0 var(--space-3);
		border-bottom: 1px solid var(--border-subtle);
		background: var(--surface-shell);
	}

	.crumbs {
		flex: 1;
		min-width: 0;
		padding-left: var(--space-1);
	}

	.search {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		width: clamp(200px, 22vw, 300px);
		height: 34px;
		margin-left: var(--space-2);
		padding: 0 6px 0 var(--space-3);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-search);
		color: var(--text-muted);
		text-align: left;
	}

	.search:hover {
		border-color: var(--text-faint);
	}

	.placeholder {
		flex: 1;
		font-size: var(--text-body);
	}

	.env-offline {
		padding: var(--space-4) var(--page-gutter) 0;
	}

	main {
		flex: 1;
		min-width: 0;
		padding: var(--space-5) var(--page-gutter) var(--space-8);
		outline: none;
	}

	@media (max-width: 767px) {
		main {
			padding: var(--space-4) var(--space-3) var(--space-8);
		}

		.topbar {
			padding: 0 var(--space-2);
			gap: var(--space-1);
		}

		.env-offline {
			padding: var(--space-3) var(--space-3) 0;
		}
	}
</style>
