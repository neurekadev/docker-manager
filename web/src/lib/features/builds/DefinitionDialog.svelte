<script lang="ts">
	// Create or edit a saved build definition (#33): a name, a description
	// and the Git build source, re-run on demand (no scheduled rebuilds in
	// v1). Edits send If-Match with the loaded revision. A new definition
	// can start from a build's source ("Save as definition" on a build).
	import { untrack } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap } from '$lib/api/client';
	import type { EnvTarget } from '$lib/api/multi-env';
	import { queryKeys, type BuildDefinition } from '$lib/api/queries';
	import { Button, Dialog, Select, TextField, errorMessage, fieldError, toast } from '$lib/ui';
	import BuildSourceForm from './BuildSourceForm.svelte';
	import {
		type BuildSource,
		emptyForm,
		formFromSource,
		isComplete,
		toSource,
		validateSource,
		type SourceForm
	} from './source';

	interface Props {
		open?: boolean;
		/** Edit this definition; absent: create one. */
		definition?: BuildDefinition | null;
		environments: EnvTarget[];
		environmentId?: string;
		/** A new definition starts from this source (a build's). */
		source?: BuildSource;
	}

	let {
		open = $bindable(false),
		definition = null,
		environments,
		environmentId,
		source
	}: Props = $props();
	const queryClient = useQueryClient();

	let env = $state('');
	let name = $state('');
	let description = $state('');
	let form = $state<SourceForm>(emptyForm());
	let busy = $state(false);
	let failure = $state<unknown>(null);

	$effect(() => {
		if (!open) return;
		untrack(() => {
			env = definition?.environmentId ?? environmentId ?? environments[0]?.id ?? '';
			name = definition?.name ?? '';
			description = definition?.description ?? '';
			form = formFromSource(definition?.source ?? source);
			failure = null;
		});
	});

	const errors = $derived(validateSource(form));
	const valid = $derived(
		!!env && !!name.trim() && isComplete(form) && Object.keys(errors).length === 0
	);
	const serverError = (f: string) => fieldError(failure, `body.source.${f}`);

	async function save() {
		busy = true;
		failure = null;
		try {
			if (definition) {
				await unwrap(
					api.PATCH(
						'/api/v1/environments/{environmentId}/build-definitions/{definitionId}',
						{
							params: {
								path: {
									environmentId: definition.environmentId,
									definitionId: definition.id
								},
								header: { 'If-Match': `"${definition.revision ?? 0}"` }
							},
							body: {
								name: name.trim(),
								description: description.trim(),
								source: toSource(form)
							}
						}
					)
				);
				toast.success(`Saved ${name.trim()}`);
			} else {
				await unwrap(
					api.POST('/api/v1/environments/{environmentId}/build-definitions', {
						params: { path: { environmentId: env } },
						body: {
							name: name.trim(),
							description: description.trim() || undefined,
							source: toSource(form)
						}
					})
				);
				toast.success(`Saved the build definition ${name.trim()}`);
			}
			void queryClient.invalidateQueries({ queryKey: queryKeys.images.all });
			open = false;
		} catch (e) {
			failure = e;
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title={definition ? `Edit ${definition.name}` : 'New build definition'}
	size="lg"
	dismissible={!busy}
>
	<form
		class="form"
		id="definition-form"
		onsubmit={(e) => {
			e.preventDefault();
			if (valid) void save();
		}}
	>
		<div class="head">
			{#if !definition && environments.length > 1}
				<Select
					label="Environment"
					bind:value={env}
					options={environments.map((e) => ({ value: e.id, label: e.name }))}
				/>
			{/if}
			<TextField
				label="Name"
				required
				bind:value={name}
				placeholder="silo-web"
				error={fieldError(failure, 'body.name')}
			/>
			<TextField label="Description" bind:value={description} description="Optional." />
		</div>
		<BuildSourceForm bind:form {errors} {serverError} />
		{#if failure && !fieldError(failure, 'body.name')}
			<p class="error" role="alert">
				{(failure as { status?: number }).status === 412
					? 'Someone changed this definition meanwhile. Close the dialog and open it again to edit the current version.'
					: errorMessage(failure)}
			</p>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button
			type="submit"
			form="definition-form"
			variant="primary"
			loading={busy}
			disabled={!valid}>{definition ? 'Save changes' : 'Save definition'}</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-5);
	}

	.head {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: var(--space-4);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
