<script lang="ts">
	// The signed-in app shell (#22): sidebar (≥1280 px full, 1024–1279 px icon
	// rail, <1024 px off-canvas drawer), top bar (sidebar toggle, breadcrumbs,
	// live indicator, running jobs, ⌘K search, notices, user menu), the offline banner
	// when the live stream has been down for 5 s, the banner of a manager that
	// moves to a new server (read-only, MoveBanner), and the offline-environment
	// banner for the selected environment (not on that environment's own
	// page, which says it itself). A single crumb that repeats the page
	// title is left out. Visited pages feed the palette's "Recent".
	import type { Snippet } from 'svelte';
	import { MediaQuery } from 'svelte/reactivity';
	import { untrack } from 'svelte';
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
		recentJobsQuery
	} from '$lib/api/queries';
	import { activeAlertsQuery } from '$lib/features/alerts/queries';
	import { jobKindLabel } from '$lib/features/jobs/labels';
	import MoveBanner from '$lib/features/managermove/MoveBanner.svelte';
	import { liveStatus } from '$lib/live/status.svelte';
	import { bannerDelay, bannerText } from './live-banner';
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
	import RunningJobs from './RunningJobs.svelte';
	import Sidebar from './Sidebar.svelte';
	import UserMenu from './UserMenu.svelte';
	import { environmentSelection } from './environment.svelte';
	import { accessOf, activeNav, hasAny, isRestricted, palettePages, visibleNav } from './nav';
	import { jobNotices, notices } from './notices.svelte';
	import { pageState } from './page.svelte';
	import { recentPages } from './recent.svelte';

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
	const pages = $derived(palettePages(access));
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

	// The bell (#25 Q6, #159): the manager's active alerts (disks and RAID,
	// offline environments, failed scheduled jobs, available updates) and
	// notices of the user's own jobs from the recent jobs, both refreshed
	// by live events. Dismissals made in another tab apply here too.
	const signedIn = $derived(!!perms.data && !isRestricted(access));
	const activeAlerts = createQuery(() => ({ ...activeAlertsQuery(), enabled: signedIn }));
	$effect(() => {
		notices.setAlerts(activeAlerts.data ?? []);
	});
	$effect(() => notices.listen());
	const recentJobs = createQuery(() => ({ ...recentJobsQuery(20), enabled: signedIn }));
	const feedJobNotices = jobNotices(() => user.id, jobKindLabel);
	$effect(() => {
		if (recentJobs.data) feedJobNotices(recentJobs.data.items);
	});

	// "Recent" in the palette: every visited path, named by the title its
	// page registers (pageState changes when a page calls usePage).
	$effect(() => {
		const path = page.url.pathname;
		untrack(() => recentPages.visit(path));
	});
	$effect(() => {
		const title = pageState.current.title;
		// The default title means no page registered one (yet).
		if (title !== 'Docker Manager') untrack(() => recentPages.name(page.url.pathname, title));
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
		const all =
			meta.environmentScoped && selected
				? [{ label: selected.name, href: routes.environment(selected.id) }, ...meta.crumbs]
				: meta.crumbs;
		// A lone crumb repeating the page's title (Dashboard, Environments) adds nothing.
		return all.length === 1 && all[0].label === meta.title ? [] : all;
	});

	// The environment page shows its own offline notice.
	const onOwnEnvironmentPage = $derived(
		!!selected &&
			page.route.id === '/(app)/environments/[environmentId]' &&
			page.params.environmentId === selected.id
	);

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
			<RunningJobs enabled={!!perms.data && !isRestricted(access)} compact={narrow.current} />
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
			<NoticesBell alertsHref={signedIn ? routes.alerts() : undefined} />
			<UserMenu {user} {onsignout} />
		</header>

		{#if liveBanner}
			{@const text = bannerText(liveStatus.tooManyStreams)}
			<Notice tone="offline" icon={CloudOff} title={text.title} bar>{text.body}</Notice>
		{/if}
		<MoveBanner owner={access.owner} />
		{#if selected && !selected.online && !onOwnEnvironmentPage}
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
		<Sidebar items={nav} activeId={active?.id} drawer onnavigate={() => (drawerOpen = false)}>
			{#snippet switcher()}{@render envSwitcher(false)}{/snippet}
		</Sidebar>
	</Drawer>
{/if}

<CommandPalette
	bind:open={paletteOpen}
	{pages}
	environmentId={environmentSelection.id}
	environmentName={selected?.name}
	onnavigate={(href) => goto(href)}
	{access}
	recent={recentPages.items}
	currentPath={page.url.pathname}
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
