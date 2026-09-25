<script lang="ts">
	// "About this DockYard" on the settings overview (#4 GET/PATCH /settings): the
	// display name (renamed in place with settings.manage), the version and
	// the read-only deployment configuration from the manager's environment
	// variables.
	import Pencil from '@lucide/svelte/icons/pencil';
	import { Button, Card, Notice, TextField } from '$lib/ui';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import Facts from '$lib/features/common/Facts.svelte';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
	import { deploymentFacts, instanceNameProblem, MAX_INSTANCE_NAME } from './model';
	import type { InstanceSettings } from './queries';

	let {
		settings,
		canEdit = false,
		version,
		onsave
	}: {
		settings: InstanceSettings;
		canEdit?: boolean;
		/** Version and short commit of the manager, when known. */
		version?: string;
		/** Saves the new name; rejects with the API error. */
		onsave: (name: string) => Promise<void>;
	} = $props();

	let editing = $state(false);
	let name = $state('');
	let saving = $state(false);
	let fieldError = $state<string | null>(null);
	let saveError = $state<string | null>(null);

	const facts = $derived([
		...(version ? [{ label: 'Version', value: version, mono: true }] : []),
		...deploymentFacts(settings)
	]);

	function start() {
		name = settings.name;
		fieldError = saveError = null;
		editing = true;
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		fieldError = instanceNameProblem(name);
		saveError = null;
		if (fieldError) return;
		saving = true;
		try {
			await onsave(name.trim());
			editing = false;
		} catch (err) {
			fieldError = fieldErrors(err)['body.name'] ?? null;
			saveError = fieldError ? null : actionError(err);
		} finally {
			saving = false;
		}
	}
</script>

<Card title="About this DockYard" id="instance">
	{#snippet actions()}
		{#if canEdit && !editing}
			<Button size="sm" variant="ghost" icon={Pencil} onclick={start}>Rename</Button>
		{/if}
	{/snippet}
	{#if editing}
		<form onsubmit={save} novalidate>
			<TextField
				label="Name"
				description="Shown in the settings of this DockYard. 1 to {MAX_INSTANCE_NAME} characters."
				bind:value={name}
				error={fieldError}
				maxlength={MAX_INSTANCE_NAME}
				required
			/>
			{#if saveError}<Notice tone="danger" title="Not renamed" live="alert"
					>{saveError}</Notice
				>{/if}
			<FormFooter>
				<Button variant="ghost" onclick={() => (editing = false)}>Cancel</Button>
				<Button type="submit" variant="primary" loading={saving}>Rename DockYard</Button>
			</FormFooter>
		</form>
	{:else}
		<p class="name">{settings.name}</p>
	{/if}
	<div class="facts">
		<Facts items={facts} />
	</div>
	<p class="muted note">
		The deployment settings come from the manager's environment variables. Change them where
		DockYard is deployed and restart the manager.
	</p>
</Card>

<style>
	.name {
		margin: 0;
		color: var(--text-strong);
		font-size: var(--text-kpi);
		line-height: var(--leading-kpi);
		font-weight: 600;
		overflow-wrap: anywhere;
	}

	.facts {
		margin-top: var(--space-4);
	}

	.note {
		margin: var(--space-4) 0 0;
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}
</style>
