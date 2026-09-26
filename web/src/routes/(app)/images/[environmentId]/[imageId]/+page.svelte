<script lang="ts">
	// Image detail (#6): tags and digests, platform, configuration, the
	// containers using it (removal is refused while any do) and labels.
	// Actions: tag, remove (server preview first), create a container.
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Box from '@lucide/svelte/icons/box';
	import Clock from '@lucide/svelte/icons/clock';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Plus from '@lucide/svelte/icons/plus';
	import Server from '@lucide/svelte/icons/server';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import Tag from '@lucide/svelte/icons/tag';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { ApiRequestError } from '$lib/api/client';
	import { imageQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		ErrorState,
		JobProgress,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Skeleton,
		StatusBadge,
		formatBytes,
		formatRelative,
		type MetaItem
	} from '$lib/ui';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
	import ImageActionHost from '$lib/features/resources/ImageActionHost.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import { activeJobs, resourceKey } from '$lib/features/resources/jobs.svelte';
	import { joinCommand, shortDigest } from '$lib/features/resources/model';
	import { can } from '$lib/features/resources/permissions';
	import { protectionLabel, sentence } from '$lib/features/resources/refusals';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	const env = $derived(page.params.environmentId ?? '');
	const id = $derived(page.params.imageId ?? '');
	const scope = useEnvironmentScope();
	const envName = $derived(scope.name(env));
	const q = createQuery(() => imageQuery(env, id));
	const im = $derived(q.data);
	const title = $derived(im ? (im.repoTags[0] ?? shortDigest(im.id)) : shortDigest(id));
	let host = $state<ImageActionHost>();

	usePage(() => ({
		title,
		crumbs: [{ label: 'Images', href: routes.images() }, { label: envName }, { label: title }]
	}));

	const offline = $derived(
		q.error instanceof ApiRequestError && q.error.apiError?.code === 'environment_offline'
	);
	const notFound = $derived(q.error instanceof ApiRequestError && q.error.status === 404);
	const job = $derived(im ? activeJobs.byKey[resourceKey('image', env, im.id)] : undefined);
	const d = $derived(im?.details);

	const meta = $derived<MetaItem[]>(
		im
			? [
					{ icon: Server, label: envName },
					...(im.size ? [{ icon: HardDrive, label: formatBytes(im.size) }] : []),
					...(im.createdAt
						? [
								{
									icon: Clock,
									label: `Built ${formatRelative(im.createdAt)}`,
									title: im.createdAt
								}
							]
						: [])
				]
			: []
	);
	const facts = $derived<Fact[]>(
		im
			? [
					{ label: 'Image ID', value: im.id, mono: true },
					{
						label: 'Platform',
						value: d
							? [d.os, d.architecture, d.variant].filter(Boolean).join('/')
							: undefined,
						mono: true
					},
					{ label: 'Author', value: d?.author },
					{ label: 'Digests', value: im.repoDigests?.join(', '), mono: true }
				]
			: []
	);
	const config = $derived<Fact[]>(
		d
			? [
					{ label: 'Entrypoint', value: joinCommand(d.entrypoint), mono: true },
					{ label: 'Command', value: joinCommand(d.cmd), mono: true },
					{ label: 'Working directory', value: d.workingDir, mono: true },
					{ label: 'User', value: d.user || 'root' },
					{ label: 'Exposed ports', value: d.exposedPorts.join(', '), mono: true },
					{ label: 'Volumes', value: d.volumes.join(', '), mono: true },
					{
						label: 'Health check',
						value: d.hasHealthTest ? 'Defined by the image' : 'None'
					}
				]
			: []
	);
</script>

<ImageActionHost
	bind:this={host}
	environmentName={() => envName}
	onremoved={() => goto(routes.images())}
/>

<Page>
	{#if q.isPending}
		<div class="loading" aria-busy="true">
			<Skeleton height="56px" radius="lg" /><Skeleton lines={4} height="20px" />
		</div>
	{:else if offline}
		<PageHeader {title} icon={Box} color="blue" />
		<OfflineEnvironment name={envName} since={scope.environment(env)?.connectionChangedAt} />
	{:else if notFound}
		<EmptyState
			icon={Box}
			color="slate"
			title="This image is not on {envName}."
			description="It may have been removed, or you don't have access to it."
			level={1}
		>
			{#snippet actions()}<Button variant="secondary" href={routes.images()}
					>Back to images</Button
				>{/snippet}
		</EmptyState>
	{:else if q.isError}
		<ErrorState
			error={q.error}
			title="The image could not be loaded."
			onretry={() => q.refetch()}
		/>
	{:else if im}
		<PageHeader {title} description="Image {shortDigest(im.id)}" icon={Box} color="blue" {meta}>
			{#snippet status()}
				{#if im.inUse}<Badge tone="ok" dot>In use</Badge>{:else}<Badge>Unused</Badge>{/if}
				{#if im.protection}<ProtectionBadge protection={im.protection} />{/if}
			{/snippet}
			{#snippet actions()}
				{#if im.repoTags[0] && scope.can('container.create', env)}
					<Button
						variant="primary"
						icon={Plus}
						href={routes.newContainer(env, im.repoTags[0])}>Create container</Button
					>
				{/if}
				{#if can(im.actions, 'image.tag')}
					<Button variant="secondary" icon={Tag} onclick={() => host?.request(im, 'tag')}
						>Tag</Button
					>
				{/if}
				{#if can(im.actions, 'image.remove')}
					<Button
						variant="danger-soft"
						icon={Trash2}
						onclick={() => host?.request(im, 'remove')}>Remove</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if im.protection}
			<Notice
				tone="info"
				icon={ShieldCheck}
				title={protectionLabel(im.protection)}
				live="none"
			>
				{sentence(im.protection.reason)} Docker Manager never removes it, for anyone. Use Docker
				on the host if you really need to.
			</Notice>
		{/if}
		{#if job}<JobProgress watcher={job} variant="inline" notices={null} />{/if}

		<div class="grid">
			<Card title="Details"><Facts items={facts} label="Details of {title}" /></Card>
			<Card title="Configuration">
				{#if d}<Facts items={config} label="Configuration of {title}" />
				{:else}<p class="muted">
						Viewing the configuration needs the image read permission.
					</p>{/if}
			</Card>
			<Card title="Tags">
				{#if im.repoTags.length}
					<ul class="chips" role="list">
						{#each im.repoTags as t (t)}<li class="mono">{t}</li>{/each}
					</ul>
				{:else}
					<p class="muted">
						Untagged: a newer image took its tag. Unused untagged images are safe to
						remove.
					</p>
				{/if}
			</Card>
			<Card title="Used by">
				{#if im.usedBy?.length}
					<ul class="list" role="list">
						{#each im.usedBy as c (c.id)}
							<li>
								<a href={routes.container(env, c.name)}>{c.name}</a>
								{#if c.state}<StatusBadge status={c.state} />{/if}
							</li>
						{/each}
					</ul>
				{:else}<p class="muted">No container uses this image.</p>{/if}
			</Card>
		</div>
		{#if im.labels && Object.keys(im.labels).length}
			<Card title="Labels">
				<Facts
					items={Object.entries(im.labels)
						.sort(([a], [b]) => a.localeCompare(b))
						.map(([k, v]) => ({ label: k, value: v, mono: true }))}
					label="Labels of {title}"
				/>
			</Card>
		{/if}
	{/if}
</Page>

<style>
	.loading {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-4);
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.chips li {
		padding: 2px 8px;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
		font-size: var(--text-caption);
	}

	.list {
		display: grid;
		gap: var(--space-2);
	}

	.list li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}

	.list a {
		color: var(--accent-text);
		text-decoration: none;
	}

	@media (max-width: 1023px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}
</style>
