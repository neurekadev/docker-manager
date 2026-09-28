<script lang="ts">
	// Duplicate a template (template registry): copies a published version of
	// this instance's or a registry's template into a new private template
	// you can change. The source never changes.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { routes } from '$lib/routes';
	import { Button, Dialog, Select, TextField, errorView, toast } from '$lib/ui';
	import { duplicateTemplate } from './actions';
	import { templateKeys } from './queries';

	interface Props {
		open?: boolean;
		source: {
			/** The registry (empty: this instance). */
			instanceId?: string;
			templateId: string;
			name: string;
			versions: { number: number; label: string }[];
		};
	}

	let { open = $bindable(false), source }: Props = $props();
	const queryClient = useQueryClient();
	let name = $state('');
	let version = $state('');
	let saving = $state(false);
	let error = $state<string | null>(null);

	$effect(() => {
		if (open) {
			name = `${source.name} (copy)`.slice(0, 100);
			version = String(source.versions[0]?.number ?? '');
			error = null;
		}
	});

	async function duplicate() {
		saving = true;
		error = null;
		try {
			const t = await duplicateTemplate({
				name: name.trim(),
				instanceId: source.instanceId || undefined,
				templateId: source.templateId,
				version: Number(version)
			});
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			toast.success(`Created ${t.name} from ${source.name}`);
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
	title="Duplicate {source.name}"
	description="Creates a private template from a published version. You can change its files, then publish your own versions."
	size="md"
	dismissible={!saving}
>
	<form
		id="duplicate-template"
		class="form"
		onsubmit={(e) => {
			e.preventDefault();
			void duplicate();
		}}
	>
		<TextField label="Name" bind:value={name} required maxlength={100} {error} />
		<Select
			label="Version"
			bind:value={version}
			options={source.versions.map((v, i) => ({
				value: String(v.number),
				label: i === 0 ? `${v.label} (latest)` : v.label
			}))}
		/>
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={saving}>Cancel</Button>
		<Button
			variant="primary"
			type="submit"
			form="duplicate-template"
			loading={saving}
			disabled={!name.trim() || !version}>Duplicate</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
	}
</style>
