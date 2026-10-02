<script lang="ts">
	// Add a template source (a template registry, owner): another Docker
	// Manager's address. Its public templates are read right away; adding
	// an instance that was removed before brings back the icons of stacks
	// created from its templates.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { Button, Dialog, TextField, errorView, toast } from '$lib/ui';
	import { addRegistry } from './actions';
	import { templateKeys } from './queries';

	let { open = $bindable(false) }: { open?: boolean } = $props();
	const queryClient = useQueryClient();
	let url = $state('');
	let saving = $state(false);
	let error = $state<string | null>(null);

	$effect(() => {
		if (!open) {
			url = '';
			error = null;
		}
	});

	async function add() {
		saving = true;
		error = null;
		try {
			const r = await addRegistry(url.trim());
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			toast.success(
				`Added ${r.name} with ${r.templates} ${r.templates === 1 ? 'template' : 'templates'}`
			);
			open = false;
		} catch (e) {
			const v = errorView(e);
			switch (v.code) {
				case 'template_registry_exists':
					error = 'This Docker Manager is a template source already.';
					break;
				case 'template_registry_is_self':
					error =
						"This is this Docker Manager's own address: its templates are listed already.";
					break;
				default:
					error = v.message;
			}
		} finally {
			saving = false;
		}
	}
</script>

<Dialog
	bind:open
	title="Add a Template Source"
	description="Browse and use the public templates of another Docker Manager. Enter its address; it must use HTTPS."
	size="md"
	dismissible={!saving}
>
	<form
		id="add-registry"
		class="form"
		onsubmit={(e) => {
			e.preventDefault();
			void add();
		}}
	>
		<TextField
			label="Address"
			bind:value={url}
			required
			placeholder="https://docker.example.com"
			description="The other Docker Manager's address, or its /registry page."
			{error}
			oninput={() => (error = null)}
		/>
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={saving}>Cancel</Button>
		<Button
			variant="primary"
			type="submit"
			form="add-registry"
			loading={saving}
			disabled={!url.trim()}>Add Template Source</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
	}
</style>
