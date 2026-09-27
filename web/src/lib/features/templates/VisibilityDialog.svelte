<script lang="ts">
	// Make a template public or private (template registry). Public lists
	// its published versions in this instance's registry: anyone with the
	// registry URL can read every file, .env included, so confirming needs
	// the explicit acknowledgement (the server refuses without it).
	import { useQueryClient } from '@tanstack/svelte-query';
	import { Checkbox, ConfirmDialog, toast } from '$lib/ui';
	import { setVisibility } from './actions';
	import { templateKeys, type Template } from './queries';

	interface Props {
		open?: boolean;
		template: Template;
	}

	let { open = $bindable(false), template }: Props = $props();
	const queryClient = useQueryClient();
	let acknowledged = $state(false);
	const toPublic = $derived(template.visibility !== 'public');

	$effect(() => {
		if (open) acknowledged = false;
	});

	// Errors stay in the dialog (ConfirmDialog shows them).
	async function confirm() {
		const next = await setVisibility(
			template,
			toPublic ? 'public' : 'private',
			toPublic && acknowledged
		);
		queryClient.setQueryData(templateKeys.detail(template.id), next);
		void queryClient.invalidateQueries({ queryKey: templateKeys.all });
		toast.success(toPublic ? `${template.name} is public` : `${template.name} is private`);
	}
</script>

{#if toPublic}
	<ConfirmDialog
		bind:open
		title="Make {template.name} public?"
		message="Its published versions appear in this instance's public registry. Other Docker Manager instances that add the registry URL can browse them and create stacks from them."
		consequences={[
			'Anyone with the registry URL can download every file of every published version, including .env.',
			'Remove passwords, keys and tokens from the draft and publish a clean version first.',
			'Making it private again hides it from the registry; copies others already downloaded stay with them.'
		]}
		confirmLabel="Make public"
		canConfirm={acknowledged}
		onconfirm={confirm}
	>
		<Checkbox
			bind:checked={acknowledged}
			label="I understand every file, .env included, becomes public"
		/>
	</ConfirmDialog>
{:else}
	<ConfirmDialog
		bind:open
		title="Make {template.name} private?"
		message="It disappears from this instance's public registry. Only people on this instance with access to it can use it."
		consequences={[
			'Other instances stop offering it after their next registry sync.',
			'Stacks created from it anywhere keep working.'
		]}
		confirmLabel="Make private"
		onconfirm={confirm}
	/>
{/if}
