<script lang="ts">
	// Maintenance defaults (#14) in a dialog: the rules new prune policies
	// start with. Changing them never changes existing policies. Editing
	// needs settings.manage; reading settings.read. Render it only while
	// open ({#if}): each opening loads the saved defaults.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap } from '$lib/api/client';
	import { Button, Dialog, Notice, toast } from '$lib/ui';
	import { ifMatch } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import RuleList from './RuleList.svelte';
	import {
		normalizeRules,
		ruleProblem,
		type MaintenanceDefaults,
		type MaintenanceRule
	} from './model';
	import { maintenanceDefaultsQuery, maintenanceKeys } from './queries';

	let { open = $bindable(true), canEdit }: { open?: boolean; canEdit: boolean } = $props();

	const qc = useQueryClient();
	const defaults = createQuery(() => ({
		...maintenanceDefaultsQuery(),
		refetchOnWindowFocus: false
	}));

	let rules = $state<MaintenanceRule[] | null>(null);
	let base = $state<MaintenanceDefaults | null>(null);
	let dirty = $state(false);
	let busy = $state(false);
	let error = $state<unknown>(null);

	// Load once; a live refresh while editing does not replace the edits.
	$effect(() => {
		const d = defaults.data;
		if (!d) return;
		untrack(() => {
			if (!dirty) {
				base = d;
				rules = normalizeRules(d.rules);
			}
		});
	});

	useUnsaved(
		() => 'Maintenance defaults',
		() => dirty && !busy
	);

	const problems = $derived((rules ?? []).map(ruleProblem).filter(Boolean));

	async function save() {
		if (!rules || !base) return;
		busy = true;
		error = null;
		try {
			const saved = await unwrap(
				api.PATCH('/api/v1/maintenance-defaults', {
					params: { header: { 'If-Match': ifMatch(base.revision) } },
					body: { rules }
				})
			);
			dirty = false;
			qc.setQueryData(maintenanceKeys.defaults, saved);
			toast.success('Saved the maintenance defaults', {
				body: 'New policies start with these rules; existing policies keep theirs.'
			});
			open = false;
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title="Maintenance Defaults"
	description="The rules new maintenance policies start with. Existing policies keep theirs."
	size="xl"
	dismissible={!busy}
>
	<QueryView
		query={defaults}
		errorTitle="The maintenance defaults could not be loaded."
		deniedTitle="You can't see the maintenance defaults."
		deniedDescription="Ask the owner of this Docker Manager for the View Settings permission."
	>
		{#snippet children(d)}
			{#if error}
				<div class="error">
					<Notice tone="danger" title="The defaults were not saved" live="alert"
						>{actionError(error)}</Notice
					>
				</div>
			{/if}
			<RuleList
				rules={rules ?? []}
				columns={2}
				info={d.categories}
				disabled={!canEdit}
				onchange={(i, nr) => {
					if (!rules) return;
					rules[i] = nr;
					dirty = true;
				}}
			/>
		{/snippet}
	</QueryView>
	{#snippet footer()}
		<Button variant="ghost" disabled={busy} onclick={() => (open = false)}
			>{canEdit ? 'Cancel' : 'Close'}</Button
		>
		{#if canEdit}
			<Button
				variant="primary"
				loading={busy}
				disabled={!dirty || problems.length > 0}
				onclick={save}>Save Defaults</Button
			>
		{/if}
	{/snippet}
</Dialog>

<style>
	.error {
		margin-bottom: var(--space-4);
	}
</style>
