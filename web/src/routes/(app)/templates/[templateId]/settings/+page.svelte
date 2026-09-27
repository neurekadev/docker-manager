<script lang="ts">
	// Template detail, Settings tab (template registry): details and tags
	// (template.manage), the icon (template.manage), visibility
	// (template.publish) and deletion (template.remove). Each card shows
	// only with its capability.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { untrack } from 'svelte';
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
	}

	const tags = $derived(parseTags(tagText));
	const tagError = $derived(tags.map(tagProblem).find((p) => p) || null);
	const dirty = $derived(
		!!t &&
			(name.trim() !== t.name ||
				description.trim() !== (t.description ?? '') ||
				tags.join(',') !== (t.tags ?? []).join(','))
	);
	let saving = $state(false);
	let saveError = $state<string | null>(null);

	async function save() {
		if (!t || tagError) return;
		saving = true;
		saveError = null;
		try {
			const next = await patchTemplate(t, {
				name: name.trim(),
				description: description.trim(),
				tags
			});
			queryClient.setQueryData(templateKeys.detail(t.id), next);
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			toast.success(`Saved details of ${next.name}`);
		} catch (e) {
			const v = errorView(e);
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

{#if t}
	<div class="cards">
		{#if can('template.manage')}
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
					{#if saveError}<p class="error" role="alert">{saveError}</p>{/if}
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

			<Card title="Icon" id="icon">
				<IconUpload template={t} />
			</Card>
		{/if}

		{#if can('template.publish')}
			<Card title="Visibility" id="visibility">
				<div class="body">
					{#if t.visibility === 'public'}
						<p>
							<strong>Public.</strong> Its published versions are listed in this instance's
							public registry. Anyone with the registry URL can download every file of them,
							including .env.
						</p>
						<div class="row">
							<Button onclick={() => (visibilityOpen = true)}>Make private</Button>
						</div>
					{:else}
						<p>
							<strong>Private.</strong> Only people on this instance with access to it can
							use it. Make it public to share it with other Docker Manager instances through
							this instance's registry URL.
						</p>
						<div class="row">
							<Button onclick={() => (visibilityOpen = true)}>Make public</Button>
						</div>
					{/if}
				</div>
			</Card>
			<VisibilityDialog bind:open={visibilityOpen} template={t} />
		{/if}

		{#if can('template.remove')}
			<Card title="Delete template" id="delete">
				<div class="body">
					<p>
						Deletes the draft, the icon and every version. Stacks created from it keep
						working with their own files.
					</p>
					<div class="row">
						<Button variant="danger" onclick={() => (deleteOpen = true)}
							>Delete template</Button
						>
					</div>
				</div>
			</Card>
			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Delete template {t.name}?"
				consequences={[
					`Deletes the draft files and ${t.versions} published ${t.versions === 1 ? 'version' : 'versions'}.`,
					'Other instances stop offering it after their next registry sync.',
					'Stacks created from it keep working, but no longer show its icon.'
				]}
				confirmText={t.name}
				confirmLabel="Delete template"
				onconfirm={remove}
			/>
		{/if}
	</div>
{/if}

<style>
	.cards {
		display: grid;
		gap: var(--space-4);
		max-width: 760px;
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

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
