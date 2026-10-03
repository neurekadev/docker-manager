<script lang="ts">
	// New template (template registry): name, description and tags. The
	// template starts private with a starter compose.yaml in its draft; the
	// dialog opens the draft's files next, where the user adds .env and the
	// files next to it.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { routes } from '$lib/routes';
	import {
		Button,
		Dialog,
		Notice,
		TagInput,
		TextArea,
		TextField,
		errorView,
		fieldError,
		toast
	} from '$lib/ui';
	import { createTemplate } from './actions';
	import { MAX_TAGS, normalizeTag, tagProblem } from './model';
	import { templateKeys } from './queries';

	let { open = $bindable(false) }: { open?: boolean } = $props();
	const queryClient = useQueryClient();

	let name = $state('');
	let description = $state('');
	let tags = $state<string[]>([]);
	let saving = $state(false);
	let error = $state<unknown>(null);

	const tagError = $derived(tags.map(tagProblem).find((p) => p) || null);

	$effect(() => {
		if (!open) {
			name = description = '';
			tags = [];
			error = null;
		}
	});

	async function create() {
		if (tagError) return;
		saving = true;
		error = null;
		try {
			const t = await createTemplate({
				name: name.trim(),
				description: description.trim(),
				tags
			});
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			toast.success(`Created template ${t.name}`);
			open = false;
			await goto(routes.template(t.id, 'files'));
		} catch (e) {
			error = e;
		} finally {
			saving = false;
		}
	}

	const view = $derived(error ? errorView(error) : null);
</script>

<Dialog
	bind:open
	title="New Template"
	description="It starts private, with a starter compose.yaml you edit next."
	size="md"
	dismissible={!saving}
>
	<form
		class="form"
		id="create-template"
		onsubmit={(e) => {
			e.preventDefault();
			void create();
		}}
	>
		<TextField
			label="Name"
			bind:value={name}
			required
			maxlength={100}
			error={fieldError(error, 'body.name') ??
				(view?.code === 'template_name_taken'
					? 'Another template already uses this name.'
					: null)}
			placeholder="Nextcloud"
		/>
		<TextArea label="Description" bind:value={description} maxlength={1024} optional />
		<TagInput
			label="Tags"
			bind:values={tags}
			normalize={normalizeTag}
			validate={tagProblem}
			max={MAX_TAGS}
			optional
			placeholder="cloud"
		/>
		{#if view && !fieldError(error, 'body.name') && view.code !== 'template_name_taken'}
			<Notice tone="danger" live="alert" title="The template was not created">
				{view.message}
			</Notice>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={saving}>Cancel</Button>
		<Button
			variant="primary"
			type="submit"
			form="create-template"
			loading={saving}
			disabled={!name.trim() || !!tagError}>Create Template</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
	}
</style>
