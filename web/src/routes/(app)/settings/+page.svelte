<script lang="ts">
	// Settings overview: my profile and API tokens for everyone; the
	// instance's name and deployment configuration (#4 /settings), sign-in
	// policy, schedule and maintenance defaults, audit log and diagnostics
	// for whoever may see them.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import Info from '@lucide/svelte/icons/info';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import LifeBuoy from '@lucide/svelte/icons/life-buoy';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import UserRound from '@lucide/svelte/icons/user-round';
	import Wrench from '@lucide/svelte/icons/wrench';
	import { healthQuery, myPermissionsQuery, sessionQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { Card, ErrorState, Skeleton, toast } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { scheduleDefaultsQuery } from '$lib/features/common/schedules';
	import Page from '$lib/features/common/Page.svelte';
	import { factorsText } from '$lib/features/access/model';
	import { myTokensQuery } from '$lib/features/access/queries';
	import InstanceCard from '$lib/features/settings/InstanceCard.svelte';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';
	import SettingsLink from '$lib/features/settings/SettingsLink.svelte';
	import { FACTOR_POLICY } from '$lib/features/settings/model';
	import {
		instanceSettingsQuery,
		saveInstanceName,
		securitySettingsQuery,
		settingsKeys
	} from '$lib/features/settings/queries';

	usePage({ title: 'Settings', crumbs: [{ label: 'Settings' }] });

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const session = createQuery(() => sessionQuery());
	const health = createQuery(() => healthQuery());
	const tokens = createQuery(() => myTokensQuery());
	const security = createQuery(() => ({ ...securitySettingsQuery(), enabled: access.owner }));
	const defaults = createQuery(() => ({
		...scheduleDefaultsQuery(),
		enabled: can(access, 'settings.read')
	}));
	const instance = createQuery(() => ({
		...instanceSettingsQuery(),
		enabled: can(access, 'settings.read')
	}));
	const qc = useQueryClient();
	const me = $derived(session.data?.user);
	const version = $derived(
		health.data
			? `${health.data.version} (build ${health.data.commit.slice(0, 12)})`
			: undefined
	);

	async function rename(name: string) {
		const current = instance.data;
		if (!current) return;
		const saved = await saveInstanceName(current, name);
		qc.setQueryData(settingsKeys.instance, saved);
		toast.success(`Renamed DockYard to ${saved.name}`);
	}
	const activeTokens = $derived((tokens.data ?? []).filter((t) => t.status === 'active').length);
</script>

<Page>
	<SettingsHeader
		title="Settings"
		description="Your profile, sign-in security and API tokens, and the settings of this DockYard."
	/>

	<section class="grid" aria-label="Your account">
		<SettingsLink
			href={routes.security()}
			icon={UserRound}
			color="blue"
			title="Profile and security"
			text={me
				? `Signs in with ${factorsText(me.factors).toLowerCase()}`
				: 'Password, authenticator app, passkeys and recovery codes'}
		/>
		<SettingsLink
			href={routes.apiTokens()}
			icon={KeyRound}
			color="violet"
			title="API tokens"
			text={tokens.data
				? `${activeTokens} active ${activeTokens === 1 ? 'token' : 'tokens'}`
				: 'Tokens for scripts and integrations'}
		/>
	</section>

	{#if access.owner || can(access, 'settings.read') || can(access, 'audit.read')}
		<section class="grid" aria-label="This DockYard">
			{#if access.owner}
				<SettingsLink
					href={routes.signInPolicy()}
					icon={ShieldCheck}
					color="green"
					title="Sign-in policy"
					text={security.data
						? `${FACTOR_POLICY[security.data.requiredFactors]}; API tokens ${security.data.apiTokensEnabled ? 'on' : 'off'}`
						: 'Passwords, required factors, invitations and API tokens'}
				/>
			{/if}
			{#if can(access, 'settings.read')}
				<SettingsLink
					href={routes.scheduleDefaults()}
					icon={CalendarClock}
					color="cyan"
					title="Schedule defaults"
					text={defaults.data
						? `Default time zone ${defaults.data.timeZone}`
						: 'Defaults of new backup, update and prune schedules'}
				/>
				<SettingsLink
					href={routes.maintenanceDefaults()}
					icon={Wrench}
					color="slate"
					title="Maintenance defaults"
					text="The prune rules new maintenance policies start with"
				/>
			{/if}
			{#if can(access, 'audit.read')}
				<SettingsLink
					href={routes.audit()}
					icon={ScrollText}
					color="indigo"
					title="Audit log"
					text="Who did what, when, and the result"
				/>
			{/if}
			{#if access.owner}
				<SettingsLink
					href={routes.diagnostics()}
					icon={LifeBuoy}
					color="rose"
					title="Diagnostics"
					text="Support bundle and internal metrics"
				/>
			{/if}
		</section>
	{/if}

	{#if can(access, 'settings.read') && instance.data}
		<InstanceCard
			settings={instance.data}
			canEdit={can(access, 'settings.manage')}
			{version}
			onsave={rename}
		/>
	{:else if can(access, 'settings.read') && !instance.isError}
		<Card title="About this DockYard"><Skeleton lines={3} /></Card>
	{:else if can(access, 'settings.read')}
		<ErrorState
			error={instance.error}
			title="The settings of this DockYard could not be loaded."
			onretry={() => instance.refetch()}
			retrying={instance.isFetching}
		/>
	{:else}
		<Card title="About this DockYard">
			{#if health.data}
				<p class="about">
					<Info size={16} aria-hidden="true" />
					<span
						>Version <span class="mono">{health.data.version}</span> (build
						<span class="mono">{health.data.commit.slice(0, 12)}</span>)</span
					>
				</p>
			{:else}
				<Skeleton lines={1} />
			{/if}
		</Card>
	{/if}
</Page>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(300px, 100%), 1fr));
		gap: var(--space-4);
	}

	.about {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}
</style>
