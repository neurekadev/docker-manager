<script lang="ts">
	// Template detail, Settings tab (template registry): details, tags and
	// links beside the icon (template.manage), visibility (template.publish) beside
	// deletion (template.remove). Each card shows only with its capability;
	// pairs sit side by side (Columns), a card without its partner takes the
	// full width.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { untrack, type Snippet } from 'svelte';
	import Columns from '$lib/features/common/Columns.svelte';
	import LinksEditor from '$lib/features/common/LinksEditor.svelte';
	import {
		cleanLinks,
		linkRows,
		linksValid,
		sameLinks,
		serverLinkProblems,
		type LinkRow,
		type LinkRowProblem
	} from '$lib/features/common/links';
	import { deleteTemplate, patchTemplate } from '$lib/features/templates/actions';
	import IconUpload from '$lib/features/templates/IconUpload.svelte';
	import { parseTags, tagProblem } from '$lib/features/templates/model';
	import { templateKeys, templateQuery, type Template } from '$lib/features/templates/queries';
	import VisibilityDialog from '$lib/features/templates/VisibilityDialog.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		DestructiveConfirm,
		Notice,
		TextArea,
		TextField,
		errorView,
		toast
	} from '$lib/ui';

	const id = $derived(page.params.templateId ?? '');
	const template = createQuery(() => templateQuery(id));
	const queryClient = useQueryClient();
	const t = $derived(template.data);
	const can = (a: string) => !!t?.actions.includes(a);

	usePage(() => ({
		title: `${t?.name ?? 'Template'} settings`,
		crumbs: [
			{ label: 'Templates', href: routes.templates() },
			{ label: t?.name ?? 'Template', href: routes.template(id) },
			{ label: 'Settings' }
		]
	}));

	// The details form edits a copy taken when the template loads.
	let loadedRevision = -1;
	let name = $state('');
	let description = $state('');
	let tagText = $state('');
	let links = $state<LinkRow[]>([]);
	let showLinkProblems = $state(false);
	let linkServerProblems = $state<{ rows: LinkRowProblem[]; list: string | null } | null>(null);
	$effect(() => {
		const cur = t;
		if (!cur || cur.revision === loadedRevision) return;
		untrack(() => reset(cur));
	});

	function reset(cur: Template) {
		loadedRevision = cur.revision ?? 0;
		name = cur.name;
		description = cur.description ?? '';
		tagText = (cur.tags ?? []).join(', ');
		links = linkRows(cur.links);
		showLinkProblems = false;
		linkServerProblems = null;
	}

	const tags = $derived(parseTags(tagText));
	const tagError = $derived(tags.map(tagProblem).find((p) => p) || null);
	const dirty = $derived(
		!!t &&
			(name.trim() !== t.name ||
				description.trim() !== (t.description ?? '') ||
				tags.join(',') !== (t.tags ?? []).join(',') ||
				!sameLinks(cleanLinks(links), t.links))
	);
	let saving = $state(false);
	let saveError = $state<string | null>(null);

	async function save() {
		if (!t || tagError) return;
		if (!linksValid(links)) {
			showLinkProblems = true;
			return;
		}
		saving = true;
		saveError = null;
		linkServerProblems = null;
		try {
			const next = await patchTemplate(t, {
				name: name.trim(),
				description: description.trim(),
				tags,
				links: cleanLinks(links)
			});
			queryClient.setQueryData(templateKeys.detail(t.id), next);
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			toast.success(`Saved details of ${next.name}`);
		} catch (e) {
			const v = errorView(e);
			const onLinks = serverLinkProblems(links, v.fields);
			if (onLinks.list || onLinks.rows.some((r) => r.label || r.url))
				linkServerProblems = onLinks;
			saveError =
				v.status === 412
					? 'Someone else changed this template meanwhile. Your edits were kept; reload the page to see theirs.'
					: v.code === 'template_name_taken'
						? 'Another template already uses this name.'
						: v.message;
		} finally {
			saving = false;
		}
	}

	let visibilityOpen = $state(false);
	let deleteOpen = $state(false);

	async function remove() {
		if (!t) return;
		await deleteTemplate(t);
		void queryClient.invalidateQueries({ queryKey: templateKeys.all });
		toast.success(`Deleted template ${t.name}`);
		await goto(routes.templates());
	}
</script>

{#snippet pair(cards: Snippet[])}
	{#if cards.length === 2}
		<Columns ratio="equal">{@render cards[0]()}{@render cards[1]()}</Columns>
	{:else if cards.length === 1}
		{@render cards[0]()}
	{/if}
{/snippet}

{#snippet detailsCard()}
	<Card title="Details" id="details">
		<form
			class="form"
			onsubmit={(e) => {
				e.preventDefault();
				void save();
			}}
		>
			<TextField label="Name" bind:value={name} required maxlength={100} />
			<TextArea label="Description" bind:value={description} maxlength={1024} />
			<TextField
				label="Tags"
				bind:value={tagText}
				description="Separate tags with commas. People browse and filter templates by them."
				error={tagError}
			/>
			<LinksEditor
				bind:rows={links}
				showAll={showLinkProblems}
				serverProblems={linkServerProblems}
				disabled={saving}
			/>
			{#if saveError}
				<Notice tone="danger" live="alert" title="The details were not saved">
					{saveError}
				</Notice>
			{/if}
			<div class="row">
				<Button
					variant="primary"
					type="submit"
					loading={saving}
					disabled={!dirty || !name.trim() || !!tagError}>Save details</Button
				>
			</div>
		</form>
	</Card>
{/snippet}

{#snippet iconCard()}
	{#if t}
		<Card title="Icon" id="icon">
			<IconUpload template={t} />
		</Card>
	{/if}
{/snippet}

{#snippet visibilityCard()}
	{#if t}
		<Card title="Visibility" id="visibility">
			<div class="body">
				{#if t.visibility === 'public'}
					<p>
						<strong>Public.</strong> Its published versions are listed on this Docker Manager's
						public page. Anyone with its address can download every file of them, including
						.env.
					</p>
					<div class="row">
						<Button onclick={() => (visibilityOpen = true)}>Make private</Button>
					</div>
				{:else}
					<p>
						<strong>Private.</strong> Only people on this Docker Manager with access to it
						can use it. Make it public to share it with other Docker Managers, which add this
						one as a template source.
					</p>
					<div class="row">
						<Button onclick={() => (visibilityOpen = true)}>Make public</Button>
					</div>
				{/if}
			</div>
		</Card>
	{/if}
{/snippet}

{#snippet deleteCard()}
	<Card title="Delete template" id="delete">
		<div class="body">
			<p>
				Deletes the draft, the icon and every version. Stacks created from it keep working
				with their own files.
			</p>
			<div class="row">
				<Button variant="danger" onclick={() => (deleteOpen = true)}>Delete template</Button
				>
			</div>
		</div>
	</Card>
{/snippet}

{#if t}
	<div class="cards">
		{@render pair(can('template.manage') ? [detailsCard, iconCard] : [])}
		{@render pair(
			[
				can('template.publish') ? visibilityCard : null,
				can('template.remove') ? deleteCard : null
			].filter((c) => c !== null)
		)}
	</div>
	{#if can('template.publish')}
		<VisibilityDialog bind:open={visibilityOpen} template={t} />
	{/if}
	{#if can('template.remove')}
		<DestructiveConfirm
			bind:open={deleteOpen}
			title="Delete template {t.name}?"
			consequences={[
				`Deletes the draft files and ${t.versions} published ${t.versions === 1 ? 'version' : 'versions'}.`,
				'Other Docker Managers stop offering it after their next sync.',
				'Stacks created from it keep working, but no longer show its icon.'
			]}
			confirmText={t.name}
			confirmLabel="Delete template"
			onconfirm={remove}
		/>
	{/if}
{/if}

<style>
	.cards {
		display: grid;
		gap: var(--space-4);
		min-width: 0;
	}

	.form,
	.body {
		display: grid;
		gap: var(--space-4);
	}

	.row {
		display: flex;
		gap: var(--space-2);
	}

	p {
		color: var(--text-default);
	}
</style>
