<script lang="ts">
	// Edit details (#22, #7): the stack's display name, description and
	// links (documentation, website, repository; checked inline with the
	// server's rules) plus each service's description. Stacks and services
	// have no icon of their own. Docker Manager metadata only (PATCH
	// /stacks/{id} with If-Match); Compose files are never touched.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import { Button, Dialog, TextArea, TextField, errorView, toast } from '$lib/ui';
	import LinksEditor from '$lib/features/common/LinksEditor.svelte';
	import {
		cleanLinks,
		linkRows,
		linksValid,
		serverLinkProblems,
		type LinkRowProblem
	} from '$lib/features/common/links';
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
	let links = $state(linkRows(initial.links));
	let showLinkProblems = $state(false);
	let linkServerProblems = $state<{ rows: LinkRowProblem[]; list: string | null } | null>(null);
	let services = $state(
		(initial.services ?? []).map((s) => ({
			name: s.name,
			description: s.description ?? ''
		}))
	);
	let saving = $state(false);
	let error = $state<string | null>(null);

	$effect(() => {
		if (!open) onclose();
	});

	async function save() {
		if (!linksValid(links)) {
			showLinkProblems = true;
			return;
		}
		saving = true;
		error = null;
		linkServerProblems = null;
		try {
			const meta: Record<string, { description: string }> = {};
			for (const s of services) meta[s.name] = { description: s.description.trim() };
			const next = await patchStack(initial, {
				displayName: displayName.trim(),
				description: description.trim(),
				links: cleanLinks(links),
				services: meta
			});
			queryClient.setQueryData(stackKeys.detail(stack.id), next);
			void queryClient.invalidateQueries({ queryKey: stackKeys.all });
			toast.success(`Saved details of ${stackTitle(next)}`);
			open = false;
		} catch (e) {
			const v = errorView(e);
			const onLinks = serverLinkProblems(links, v.fields);
			if (onLinks.list || onLinks.rows.some((r) => r.label || r.url))
				linkServerProblems = onLinks;
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
	title="Edit Details of {stackTitle(initial)}"
	description="Details are stored in Docker Manager. Compose files are never changed."
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
			label="Display Name"
			bind:value={displayName}
			description="Optional. Shown instead of the project name {initial.name}."
		/>
		<TextArea label="Description" bind:value={description} description="Optional." />
		<LinksEditor
			bind:rows={links}
			showAll={showLinkProblems}
			serverProblems={linkServerProblems}
			disabled={saving}
		/>
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
					</div>
				{/each}
			</fieldset>
		{/if}
		{#if error}<p class="error" role="alert">{error}</p>{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={saving}>Cancel</Button>
		<Button variant="primary" type="submit" form="stack-details" loading={saving}
			>Save Details</Button
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
		grid-template-columns: minmax(96px, 140px) 1fr;
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
