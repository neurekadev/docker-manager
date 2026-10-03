<script lang="ts">
	// Publish a version (template registry): freezes the draft as an
	// immutable version people create stacks from. A public template's
	// version is readable by anyone with the registry URL, .env included:
	// the dialog asks for that acknowledgement before publishing.
	import { useQueryClient } from '@tanstack/svelte-query';
	import {
		Button,
		Checkbox,
		Dialog,
		Notice,
		TextArea,
		TextField,
		errorView,
		toast
	} from '$lib/ui';
	import { publishVersion } from './actions';
	import { nextVersionLabel } from './model';
	import { templateKeys, type Template } from './queries';

	interface Props {
		open?: boolean;
		template: Template;
	}

	let { open = $bindable(false), template }: Props = $props();
	const queryClient = useQueryClient();

	let label = $state('');
	let notes = $state('');
	let acknowledged = $state(false);
	let saving = $state(false);
	let error = $state<unknown>(null);
	const isPublic = $derived(template.visibility === 'public');

	$effect(() => {
		if (open) {
			label = nextVersionLabel(template.latest?.label);
			notes = '';
			acknowledged = false;
			error = null;
		}
	});

	async function publish() {
		saving = true;
		error = null;
		try {
			const v = await publishVersion(template, {
				label: label.trim(),
				notes: notes.trim(),
				acknowledgePublic: isPublic ? acknowledged : undefined
			});
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			toast.success(`Published version ${v.label} of ${template.name}`);
			open = false;
		} catch (e) {
			error = e;
		} finally {
			saving = false;
		}
	}

	const view = $derived(error ? errorView(error) : null);
	const labelError = $derived(
		view?.code === 'template_version_label_taken'
			? 'Another version already uses this label.'
			: (view?.fields.find((f) => f.field === 'body.label')?.message ?? null)
	);
</script>

<Dialog
	bind:open
	title="Publish a Version of {template.name}"
	description="Freezes the draft's files as they are now."
	size="md"
	dismissible={!saving}
>
	<form
		class="form"
		id="publish-template"
		onsubmit={(e) => {
			e.preventDefault();
			void publish();
		}}
	>
		<TextField
			label="Version"
			bind:value={label}
			required
			maxlength={32}
			description="For example 1.2.0. Letters, digits and . + _ - only."
			error={labelError}
		/>
		<TextArea label="What Changed" bind:value={notes} maxlength={4096} optional />
		{#if isPublic}
			<Notice tone="warn" title="This template is public" live="none">
				Anyone with this Docker Manager's address can download every file of the version,
				including .env. Remove passwords and keys from the draft first, or make the template
				private.
			</Notice>
			<Checkbox
				bind:checked={acknowledged}
				label="Every file of this version, .env included, becomes public"
			/>
		{/if}
		{#if view && !labelError}
			<Notice tone="danger" live="alert" title="The version was not published">
				{view.message}
			</Notice>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={saving}>Cancel</Button>
		<Button
			variant="primary"
			type="submit"
			form="publish-template"
			loading={saving}
			disabled={!label.trim() || (isPublic && !acknowledged)}
			>Publish Version {label.trim()}</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
	}
</style>
