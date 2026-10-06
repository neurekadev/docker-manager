<script lang="ts">
	// Image detail (#6): tags and digests, platform, configuration, the
	// containers using it (removal is refused while any do) and labels
	// (system labels folded). The title is the first tag (cut with its
	// full text as tooltip), the digest short with a copy button. Actions:
	// create a container, tag; removal (server preview first) is the last
	// entry of the "More Actions" menu. Its running jobs (removal, pulls and
	// builds of its tags) come from the running list, also after a reload.
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Clock from '@lucide/svelte/icons/clock';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Plus from '@lucide/svelte/icons/plus';
	import Server from '@lucide/svelte/icons/server';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import Tag from '@lucide/svelte/icons/tag';
	import { ApiRequestError } from '$lib/api/client';
	import { imageQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		Chip,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Skeleton,
		StatusBadge,
		formatBytes,
		formatDateTime,
		formatRelative,
		type MenuEntry,
		type MetaItem
	} from '$lib/ui';
	import Columns from '$lib/features/common/Columns.svelte';
	import Digest from '$lib/features/common/Digest.svelte';
	import { onlyOneEnvironment } from '$lib/features/common/data';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
	import ImageActionHost from '$lib/features/resources/ImageActionHost.svelte';
	import LabelsCard from '$lib/features/resources/LabelsCard.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import { imageJobs } from '$lib/features/resources/object-jobs';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
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
	const jobs = useTrackedJobs(() => (im ? imageJobs(env, im) : null));
	const d = $derived(im?.details);

	const meta = $derived<MetaItem[]>(
		im
			? [
					...(onlyOneEnvironment(scope.envs.data)
						? []
						: [{ icon: Server, label: envName }]),
					...(im.size ? [{ icon: HardDrive, label: formatBytes(im.size) }] : []),
					...(im.createdAt
						? [
								{
									icon: Clock,
									label: `Built ${formatRelative(im.createdAt)}`,
									title: formatDateTime(im.createdAt)
								}
							]
						: [])
				]
			: []
	);
	const facts = $derived<Fact[]>(
		im
			? [
					{
						label: 'Platform',
						value: d
							? [d.os, d.architecture, d.variant].filter(Boolean).join('/')
							: undefined,
						mono: true
					},
					{ label: 'Author', value: d?.author }
				]
			: []
	);
	/** Removal last, after a separator (one rule on every resource page). */
	const overflow = $derived.by<MenuEntry[]>(() => {
		if (!im) return [];
		const out: MenuEntry[] = [];
		if (can(im.actions, 'image.tag'))
			out.push({ label: 'Tag', icon: Tag, onSelect: () => host?.request(im, 'tag') });
		// Docker Manager's own images are never removed: no entry (the notice says why).
		if (!im.protection && can(im.actions, 'image.remove')) {
			if (out.length) out.push({ separator: true });
			out.push({
				label: 'Remove',
				tone: 'danger',
				onSelect: () => host?.request(im, 'remove')
			});
		}
		return out;
	});
	const config = $derived<Fact[]>(
		d
			? [
					{ label: 'Entrypoint', value: joinCommand(d.entrypoint), mono: true },
					{ label: 'Command', value: joinCommand(d.cmd), mono: true },
					{ label: 'Working Directory', value: d.workingDir, mono: true },
					{ label: 'User', value: d.user || 'root' },
					{ label: 'Exposed Ports', value: d.exposedPorts.join(', '), mono: true },
					{ label: 'Volumes', value: d.volumes.join(', '), mono: true },
					{
						label: 'Health Check',
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
	onstarted={(j) => jobs.add(j)}
/>

<Page>
	{#if q.isPending}
		<div class="loading" aria-busy="true">
			<Skeleton height="56px" radius="lg" /><Skeleton lines={4} height="20px" />
		</div>
	{:else if offline}
		<PageHeader {title} {...resourceIcon('image')} />
		<OfflineEnvironment name={envName} since={scope.environment(env)?.connectionChangedAt} />
	{:else if notFound}
		<EmptyState
			icon={resourceIcon('image').icon}
			color="slate"
			title="This image is not on {envName}."
			description="It may have been removed, or you don't have access to it."
			level={1}
		>
			{#snippet actions()}<Button variant="secondary" href={routes.images()}
					>Back to Images</Button
				>{/snippet}
		</EmptyState>
	{:else if q.isError}
		<ErrorState
			error={q.error}
			title="The image could not be loaded."
			onretry={() => q.refetch()}
		/>
	{:else if im}
		<PageHeader {title} truncate {...resourceIcon('image')} {meta}>
			{#snippet status()}
				{#if im.inUse}<Badge tone="ok" dot>In Use</Badge>{:else}<Badge>Unused</Badge>{/if}
				{#if im.protection}<ProtectionBadge
						protection={im.protection}
						label="Used by Docker Manager"
					/>{/if}
			{/snippet}
			{#snippet actions()}
				{#if im.repoTags[0] && scope.can('container.create', env)}
					<Button
						variant="primary"
						icon={Plus}
						href={routes.newContainer(env, im.repoTags[0])}>Create Container</Button
					>
				{/if}
				{#if overflow.length}
					<Menu items={overflow} label="More Actions for {title}" align="end">
						{#snippet trigger(props)}
							<IconButton
								{...props}
								icon={Ellipsis}
								label="More Actions"
								variant="secondary"
							/>
						{/snippet}
					</Menu>
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
		<ActiveJobs {jobs} variant="inline" label="Running Jobs of {title}" />

		<Columns ratio="equal">
			<Card title="Details">
				<Facts items={facts} label="Details of {title}" />
				<dl class="ids">
					<div>
						<dt>Image ID</dt>
						<dd><Digest value={im.id} /></dd>
					</div>
					{#each im.repoDigests ?? [] as dg (dg)}
						<div>
							<dt>Digest</dt>
							<dd>
								<span class="repo mono" title={dg}>{dg.split('@')[0]}</span>
								<Digest value={dg.split('@')[1] ?? dg} />
							</dd>
						</div>
					{/each}
				</dl>
			</Card>
			<Card title="Configuration">
				{#if d}<Facts items={config} label="Configuration of {title}" />
				{:else}<p class="muted">
						Viewing the configuration needs the image read permission.
					</p>{/if}
			</Card>
			<Card title="Tags">
				{#if im.repoTags.length}
					<ul class="chips" role="list">
						{#each im.repoTags as t (t)}<li>
								<Chip label={t} title={t} size="sm" />
							</li>{/each}
					</ul>
				{:else}
					<p class="muted">Untagged: a newer image took its tag.</p>
				{/if}
			</Card>
			<Card title="Used By">
				{#if im.usedBy?.length}
					<ul class="list" role="list">
						{#each [...im.usedBy].sort( (a, b) => a.name.localeCompare(b.name) ) as c (c.id)}
							<li>
								<a href={routes.container(env, c.name)}>{c.name}</a>
								{#if c.state}<StatusBadge status={c.state} />{/if}
							</li>
						{/each}
					</ul>
				{:else}<p class="muted">No container uses this image.</p>{/if}
			</Card>
		</Columns>
		{#if im.labels && Object.keys(im.labels).length}
			<LabelsCard labels={im.labels} label="Labels of {title}" />
		{/if}
	{/if}
</Page>

<style>
	.loading {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	/* A long tag is cut at the card's edge (its full text is the tooltip). */
	.chips li {
		min-width: 0;
		max-width: 100%;
	}

	/* The IDs below the facts, in the same two columns. */
	.ids {
		display: grid;
		margin: 0;
	}

	.ids > div {
		display: grid;
		grid-template-columns: minmax(120px, 34%) 1fr;
		gap: var(--space-3);
		padding: 7px 0;
		border-top: 1px solid var(--border-subtle);
	}

	.ids dt {
		color: var(--text-muted);
	}

	.ids dd {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
		margin: 0;
	}

	.repo {
		overflow: hidden;
		color: var(--text-default);
		font-size: 12.5px;
		text-overflow: ellipsis;
		white-space: nowrap;
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

	@media (max-width: 767px) {
		.ids > div {
			grid-template-columns: 1fr;
			gap: 2px;
		}
	}
</style>
