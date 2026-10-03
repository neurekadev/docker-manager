<script lang="ts">
	// Settings overview: about this Docker Manager (its name, version and
	// deployment configuration) and the settings that have no tab of their
	// own (maintenance defaults). The tabs above lead to the rest (every
	// user's API tokens, sign-in policy, schedule defaults, audit log,
	// diagnostics), so they are not repeated here as cards. The caller's own
	// account and API tokens are the Profile (user menu), not Settings.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Info from '@lucide/svelte/icons/info';
	import Wrench from '@lucide/svelte/icons/wrench';
	import { healthQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, Card, ErrorState, Skeleton, toast } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import Page from '$lib/features/common/Page.svelte';
	import InstanceCard from '$lib/features/settings/InstanceCard.svelte';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';
	import {
		instanceSettingsQuery,
		saveInstanceName,
		settingsKeys
	} from '$lib/features/settings/queries';

	usePage({ title: 'Settings', crumbs: [{ label: 'Settings' }] });

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const health = createQuery(() => healthQuery());
	const instance = createQuery(() => ({
		...instanceSettingsQuery(),
		enabled: can(access, 'settings.read')
	}));
	const qc = useQueryClient();
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
		toast.success(`Renamed Docker Manager to ${saved.name}`);
	}
</script>

<Page>
	<SettingsHeader title="Settings" />

	{#if can(access, 'settings.read') && instance.data}
		<InstanceCard
			settings={instance.data}
			canEdit={can(access, 'settings.manage')}
			{version}
			onsave={rename}
		/>
	{:else if can(access, 'settings.read') && !instance.isError}
		<Card title="About This Docker Manager"><Skeleton lines={3} /></Card>
	{:else if can(access, 'settings.read')}
		<ErrorState
			error={instance.error}
			title="The settings of this Docker Manager could not be loaded."
			onretry={() => instance.refetch()}
			retrying={instance.isFetching}
		/>
	{:else}
		<Card title="About This Docker Manager">
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

	{#if can(access, 'settings.read')}
		<Card
			title="Maintenance Defaults"
			info="The cleanup rules new maintenance policies start with."
		>
			<p class="more">
				<Button size="sm" icon={Wrench} href={routes.maintenanceDefaults()}
					>Open Maintenance Defaults</Button
				>
			</p>
		</Card>
	{/if}
</Page>

<style>
	.more {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}

	.about {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}
</style>
