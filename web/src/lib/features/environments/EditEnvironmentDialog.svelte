<script lang="ts">
	// Edit an environment's display name and service address (#3): PATCH
	// with If-Match of the revision the form was opened with, so a
	// concurrent edit is reported (412) instead of overwritten.
	import { untrack } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, ApiRequestError, unwrap, type Environment } from '$lib/api/client';
	import { criticalWork, liveKeys } from '$lib/live';
	import { Button, Dialog, Notice, TextField, errorMessage, fieldError, toast } from '$lib/ui';

	let { env, open = $bindable(false) }: { env: Environment; open?: boolean } = $props();

	const qc = useQueryClient();
	let name = $state('');
	let address = $state('');
	let revision = $state(0);
	let saving = $state(false);
	let error = $state<unknown>(null);

	// Load the current values each time the dialog opens (not when a live
	// refresh changes env while the user types).
	$effect(() => {
		if (open) untrack(reload);
	});

	// Unsaved changes hold off the PWA update reload (#23).
	const dirty = $derived(open && (name !== env.name || address !== (env.serviceAddress ?? '')));
	$effect(() => {
		if (!dirty) return;
		return criticalWork.register('unsaved-edit', `${env.name} settings`);
	});

	const conflict = $derived(error instanceof ApiRequestError && error.status === 412);

	async function save(ev: SubmitEvent) {
		ev.preventDefault();
		saving = true;
		error = null;
		try {
			const out = await unwrap(
				api.PATCH('/api/v1/environments/{environmentId}', {
					params: {
						path: { environmentId: env.id },
						header: { 'If-Match': `"${revision}"` }
					},
					body: { name: name.trim(), serviceAddress: address.trim() }
				})
			);
			qc.setQueryData(liveKeys.item('environments', env.id), out);
			await Promise.all([
				qc.invalidateQueries({ queryKey: liveKeys.list('environments') }),
				qc.invalidateQueries({ queryKey: ['overview'] })
			]);
			toast.success(`Saved ${out.name}`);
			open = false;
		} catch (e) {
			error = e;
			if (e instanceof ApiRequestError && e.status === 412)
				await qc.invalidateQueries({ queryKey: liveKeys.item('environments', env.id) });
		} finally {
			saving = false;
		}
	}

	function reload() {
		name = env.name;
		address = env.serviceAddress ?? '';
		revision = env.revision ?? 0;
		error = null;
	}
</script>

<Dialog
	bind:open
	title="Edit {env.name}"
	description="The name and address are Docker Manager's; nothing on the host changes."
>
	<form id="edit-environment" class="form" onsubmit={save}>
		<TextField
			label="Name"
			bind:value={name}
			required
			maxlength={64}
			autocomplete="off"
			description="Shown everywhere in Docker Manager. The Engine host name stays as it is."
			error={fieldError(error, 'body.name')}
		/>
		<TextField
			label="Service Address"
			bind:value={address}
			mono
			maxlength={253}
			autocomplete="off"
			placeholder="192.168.1.10 or nas.home.arpa"
			description="Optional. The host name or IP address you browse to; published ports become links. Leave empty to remove the links."
			error={fieldError(error, 'body.serviceAddress')}
		/>
		{#if conflict}
			<Notice tone="warn" title="Someone else changed this environment." live="alert">
				Your values are still here. Load the current values to compare, then save again.
				{#snippet actions()}<Button size="sm" onclick={reload}>Load Current Values</Button
					>{/snippet}
			</Notice>
		{:else if error && !fieldError(error, 'body.name') && !fieldError(error, 'body.serviceAddress')}
			<Notice tone="danger" title="The changes could not be saved." live="alert"
				>{errorMessage(error)}</Notice
			>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
		<Button variant="primary" type="submit" form="edit-environment" loading={saving}
			>Save Changes</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}
</style>
