<script lang="ts">
	// Edit details (#22, #7): the stack's display name, description and
	// icon plus each service's description and icon. DockYard metadata only
	// (PATCH /stacks/{id} with If-Match); Compose files are never touched.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import { SERVICE_ICON_CATEGORY } from '$lib/design/hue';
	import { Button, Dialog, Select, TextArea, TextField, errorView, toast } from '$lib/ui';
	import { patchStack } from './actions';
	import { stackTitle } from './model';
	import { stackKeys, type Stack } from './queries';

	interface Props {
		stack: Stack;
		onclose: () => void;
	}

	let { stack, onclose }: Props = $props();
	const queryClient = useQueryClient();

	// The form edits a copy taken when the dialog opens.
	const initial = untrack(() => $state.snapshot(stack));
	let open = $state(true);
	let displayName = $state(initial.displayName ?? '');
	let description = $state(initial.description ?? '');
	let icon = $state(initial.icon ?? '');
	let services = $state(
		(initial.services ?? []).map((s) => ({
			name: s.name,
			description: s.description ?? '',
			icon: s.icon ?? ''
		}))
	);
	let saving = $state(false);
	let error = $state<string | null>(null);

	const iconOptions = [
		{ value: '', label: 'Default' },
		...Object.keys(SERVICE_ICON_CATEGORY).map((k) => ({
			value: k,
			label: k.replaceAll('-', ' ')
		}))
	];

	$effect(() => {
		if (!open) onclose();
	});

	async function save() {
		saving = true;
		error = null;
		try {
			const meta: Record<string, { description: string; icon: string }> = {};
			for (const s of services)
				meta[s.name] = { description: s.description.trim(), icon: s.icon };
			const next = await patchStack(initial, {
				displayName: displayName.trim(),
				description: description.trim(),
				icon,
				services: meta
			});
			queryClient.setQueryData(stackKeys.detail(stack.id), next);
			void queryClient.invalidateQueries({ queryKey: stackKeys.all });
			toast.success(`Saved details of ${stackTitle(next)}`);
			open = false;
		} catch (e) {
			const v = errorView(e);
			error =
				v.status === 412
					? 'Someone else changed these details meanwhile. Close this dialog and open it again to edit the current ones.'
					: v.message;
		} finally {
			saving = false;
		}
	}
</script>

<Dialog
	bind:open
	title="Edit details of {stackTitle(initial)}"
	description="Details are stored in DockYard. Compose files are never changed."
	size="md"
	dismissible={!saving}
>
	<form
		class="form"
		id="stack-details"
		onsubmit={(e) => {
			e.preventDefault();
			void save();
		}}
	>
		<TextField
			label="Display name"
			bind:value={displayName}
			description="Optional. Shown instead of the project name {initial.name}."
		/>
		<TextArea label="Description" bind:value={description} description="Optional." />
		<Select label="Icon" bind:value={icon} options={iconOptions} />
		{#if services.length}
			<fieldset class="services">
				<legend>Services</legend>
				{#each services as s (s.name)}
					<div class="svc">
						<span class="svc-name mono">{s.name}</span>
						<TextField
							label="Description of {s.name}"
							hideLabel
							bind:value={s.description}
						/>
						<Select
							label="Icon of {s.name}"
							hideLabel
							bind:value={s.icon}
							options={iconOptions}
						/>
					</div>
				{/each}
			</fieldset>
		{/if}
		{#if error}<p class="error" role="alert">{error}</p>{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={saving}>Cancel</Button>
		<Button variant="primary" type="submit" form="stack-details" loading={saving}
			>Save details</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
	}

	.services {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		border: 0;
	}

	legend {
		margin-bottom: var(--space-2);
		color: var(--text-default);
		font-weight: var(--weight-medium);
	}

	.svc {
		display: grid;
		grid-template-columns: minmax(96px, 140px) 1fr 140px;
		align-items: center;
		gap: var(--space-2);
	}

	.svc-name {
		color: var(--text-strong);
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}

	@media (max-width: 767px) {
		.svc {
			grid-template-columns: 1fr;
		}
	}
</style>
