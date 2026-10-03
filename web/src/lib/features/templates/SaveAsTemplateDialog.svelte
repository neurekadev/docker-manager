<script lang="ts">
	// Save a stack as a template (template registry): copies the chosen
	// entries of the stack's project directory from its host into a new
	// private template, or into an existing template's draft (replacing
	// it). Data folders (databases, uploads) are usually better left out:
	// templates hold at most the template size limit.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { FilesApi, type FileEntry } from '$lib/features/files/api';
	import { canAnywhere } from '$lib/features/stacks/model';
	import type { Stack } from '$lib/features/stacks/queries';
	import { routes } from '$lib/routes';
	import {
		Button,
		Checkbox,
		Dialog,
		Notice,
		RadioGroup,
		Select,
		Skeleton,
		TextField,
		errorView,
		formatBytes,
		toast
	} from '$lib/ui';
	import { saveStackAsTemplate } from './actions';
	import { templateKeys, templatesQuery } from './queries';

	interface Props {
		open?: boolean;
		stack: Stack;
	}

	let { open = $bindable(false), stack }: Props = $props();
	const queryClient = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const templates = createQuery(() => ({ ...templatesQuery(), enabled: open }));
	const title = $derived(stack.displayName || stack.name);
	const files = $derived(
		new FilesApi({ kind: 'stack', stackId: stack.id, environmentId: stack.environmentId })
	);
	const listing = createQuery(() => ({
		queryKey: ['stack-template-entries', stack.id],
		queryFn: ({ signal }: { signal: AbortSignal }) =>
			files.list('.', { sort: 'name', q: '', hidden: true }, undefined, signal),
		enabled: open,
		staleTime: 0
	}));

	const canCreate = $derived(canAnywhere(perms.data, 'template.create'));
	const writable = $derived(
		(templates.data ?? []).filter(
			(t) =>
				t.actions.includes('template.files.write') &&
				t.actions.includes('template.files.delete')
		)
	);

	let mode = $state('new');
	let name = $state('');
	let target = $state('');
	let excluded = $state<string[]>([]);
	let saving = $state(false);
	let error = $state<string | null>(null);

	$effect(() => {
		if (open) {
			mode = 'new';
			name = title;
			target = '';
			excluded = [];
			error = null;
		}
	});
	$effect(() => {
		if (open && !canCreate && writable.length) mode = 'replace';
	});

	const entries = $derived(listing.data?.items ?? []);
	const included = $derived(entries.filter((e) => !excluded.includes(e.path)));
	const ready = $derived(
		included.length > 0 && (mode === 'new' ? !!name.trim() && canCreate : !!target)
	);

	function toggle(e: FileEntry, on: boolean) {
		excluded = on ? excluded.filter((p) => p !== e.path) : [...excluded, e.path];
	}

	async function save() {
		saving = true;
		error = null;
		try {
			const t = await saveStackAsTemplate({
				stackId: stack.id,
				paths: excluded.length ? included.map((e) => e.path) : undefined,
				templateId: mode === 'replace' ? target : undefined,
				name: mode === 'new' ? name.trim() : undefined
			});
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			toast.success(
				mode === 'new'
					? `Saved ${title} as the template ${t.name}`
					: `Replaced the draft of ${t.name}`
			);
			open = false;
			await goto(routes.template(t.id, 'files'));
		} catch (e) {
			const v = errorView(e);
			error =
				v.code === 'template_name_taken'
					? 'Another template already uses this name.'
					: v.message;
		} finally {
			saving = false;
		}
	}
</script>

<Dialog
	bind:open
	title="Save {title} as a Template"
	description="Copies the stack's files into a template. The stack doesn't change."
	size="lg"
	dismissible={!saving}
>
	<form
		id="save-as-template"
		class="form"
		onsubmit={(e) => {
			e.preventDefault();
			void save();
		}}
	>
		{#if canCreate && writable.length}
			<RadioGroup
				label="Save Into"
				bind:value={mode}
				options={[
					{ value: 'new', label: 'A New Template' },
					{ value: 'replace', label: "An Existing Template's Draft (Replaces It)" }
				]}
			/>
		{/if}
		{#if mode === 'new'}
			<TextField label="Template Name" bind:value={name} required maxlength={100} />
		{:else}
			<Select
				label="Template"
				bind:value={target}
				placeholder="Choose a template"
				options={writable.map((t) => ({ value: t.id, label: t.name }))}
			/>
			<Notice tone="warn" title="The draft is replaced" live="none">
				Its current files are removed. Published versions stay as they are.
			</Notice>
		{/if}

		<fieldset class="entries">
			<legend>Files to Include</legend>
			{#if listing.isPending}
				<Skeleton lines={4} />
			{:else if listing.isError}
				<Notice tone="danger" live="alert" title="The stack's files could not be listed">
					{errorView(listing.error).message}
				</Notice>
			{:else}
				<p class="muted">
					Leave out data folders: a template holds up to 32 MB by default.
				</p>
				{#each entries as e (e.path)}
					<Checkbox
						checked={!excluded.includes(e.path)}
						onchange={(ev) => toggle(e, ev.currentTarget.checked)}
						label={e.type === 'dir' ? `${e.name}/` : e.name}
						description={e.type === 'file' ? formatBytes(e.size) : undefined}
					/>
				{/each}
			{/if}
		</fieldset>
		{#if error}
			<Notice tone="danger" live="alert" title="The stack was not saved as a template">
				{error}
			</Notice>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={saving}>Cancel</Button>
		<Button
			variant="primary"
			type="submit"
			form="save-as-template"
			loading={saving}
			disabled={!ready}>Save as Template</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
	}

	.entries {
		display: grid;
		gap: var(--space-2);
		max-height: 40vh;
		margin: 0;
		padding: 0;
		overflow-y: auto;
		border: 0;
	}

	legend {
		margin-bottom: var(--space-2);
		color: var(--text-default);
		font-weight: var(--weight-medium);
	}
</style>
