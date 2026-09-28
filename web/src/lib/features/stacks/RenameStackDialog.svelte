<script lang="ts">
	// Rename a stack's Compose project (#7). The user types the new name and
	// previews it (POST /stacks/{id}/rename-previews): what stops, the folder
	// and volume moves, the containers outside the stack that are recreated,
	// warnings and blockers. Blockers disable the confirmation. A stack whose
	// Compose file sets `name:` can only take that name (the input is locked
	// to it). Confirming types the current project name, then starts a
	// stack.rename job (If-Match) tracked in the page's job tray.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { Button, DestructiveConfirm, Notice, TextField, errorMessage } from '$lib/ui';
	import { previewRename, renameStack } from './actions';
	import { stackKeys, type Stack } from './queries';
	import {
		RENAME_RULE,
		canRename,
		containerLabel,
		lockedName,
		renameConsequences,
		renameNameError,
		volumeLabel,
		volumeNote,
		type RenamePreview
	} from './rename';
	import type { JobTray } from './tray.svelte';

	interface Props {
		open?: boolean;
		stack: Stack;
		tray: JobTray;
	}

	let { open = $bindable(false), stack, tray }: Props = $props();
	const queryClient = useQueryClient();

	let name = $state('');
	let preview = $state<RenamePreview | undefined>();
	let checking = $state(false);
	let previewError = $state<string | null>(null);
	let touched = $state(false);

	$effect(() => {
		if (!open) {
			name = '';
			preview = undefined;
			previewError = null;
			touched = false;
		}
	});

	const locked = $derived(lockedName(stack.name, preview));
	const nameProblem = $derived(touched ? renameNameError(name, stack.name) : undefined);
	const stale = $derived(!!preview && preview.to !== name.trim());
	const ready = $derived(canRename(name, stack.name, preview));

	const GENERIC = [
		'Stops the stack and starts it again under the new name.',
		'Moves its volumes and project folder to the new name.',
		'Stops and recreates containers outside the stack that use those volumes.'
	];
	const consequences = $derived(preview && !stale ? renameConsequences(preview) : GENERIC);

	async function check() {
		touched = true;
		const n = name.trim();
		if (renameNameError(n, stack.name)) return;
		checking = true;
		previewError = null;
		try {
			let p = await previewRename(stack.id, n);
			// The Compose file sets name: — the stack can only take that name.
			const only = lockedName(stack.name, p);
			if (only && only !== n) {
				name = only;
				p = await previewRename(stack.id, only);
			}
			preview = p;
		} catch (e) {
			preview = undefined;
			previewError = errorMessage(e);
		} finally {
			checking = false;
		}
	}

	async function rename() {
		const from = stack.name;
		const to = name.trim();
		const job = await renameStack(stack, to);
		tray.add(job, {
			title: `Rename ${from} to ${to}`,
			success: `Renamed ${from} to ${to}`,
			failure: `${from} was not renamed`
		});
		void queryClient.invalidateQueries({ queryKey: stackKeys.detail(stack.id) });
	}
</script>

<DestructiveConfirm
	bind:open
	title="Rename {stack.name}?"
	{consequences}
	confirmText={stack.name}
	confirmLabel="Rename stack"
	canConfirm={ready}
	size="md"
	onconfirm={rename}
>
	{#snippet extra()}
		<div class="form">
			<div class="row">
				<TextField
					label="New project name"
					bind:value={name}
					mono
					autocomplete="off"
					spellcheck="false"
					maxlength={63}
					readonly={!!locked}
					description={locked
						? `Its Compose file sets name: ${locked}. The stack can only take that name; to choose another, change name: in the file.`
						: RENAME_RULE}
					error={nameProblem}
					oninput={() => (touched = false)}
					onkeydown={(e) => {
						if (e.key === 'Enter') {
							e.preventDefault();
							void check();
						}
					}}
				/>
				<Button loading={checking} disabled={!name.trim()} onclick={check}>
					{preview && !stale ? 'Preview again' : 'Preview changes'}
				</Button>
			</div>

			{#if previewError}
				<Notice tone="danger" title="The rename cannot be previewed" live="alert"
					>{previewError}</Notice
				>
			{/if}

			{#if preview && !stale}
				{#if preview.blockers.length}
					<Notice
						tone="danger"
						title="This stack cannot be renamed to {preview.to}"
						live="alert"
					>
						<ul class="issues">
							{#each preview.blockers as b, i (i)}<li>{b.message}</li>{/each}
						</ul>
					</Notice>
				{/if}
				{#if preview.warnings.length}
					<Notice tone="warn" title="Before you rename">
						<ul class="issues">
							{#each preview.warnings as w, i (i)}<li>{w.message}</li>{/each}
						</ul>
					</Notice>
				{/if}

				<section class="plan" aria-label="What changes">
					<h3>Project folder</h3>
					<p class="mono">
						{preview.fromDir === preview.toDir
							? `${preview.fromDir} (keeps its name)`
							: `${preview.fromDir} → ${preview.toDir}`}
					</p>

					<h3>Services that stop and start again</h3>
					{#if preview.running.length}
						<p class="mono">{preview.running.join(', ')}</p>
					{:else}
						<p class="muted">None run now.</p>
					{/if}

					<h3>Volumes</h3>
					{#if preview.volumes.length}
						<ul class="items">
							{#each preview.volumes as v, i (i)}
								<li>
									<span class="mono">{volumeLabel(v)}</span>
									<span class="muted">{volumeNote(v)}</span>
								</li>
							{/each}
						</ul>
					{:else}
						<p class="muted">The stack has no volumes.</p>
					{/if}

					{#if preview.containers.length}
						<h3>Containers outside the stack (stopped and recreated)</h3>
						<ul class="items">
							{#each preview.containers as c, i (i)}
								<li>
									<span class="mono">{containerLabel(c)}</span>
									<span class="muted"
										>{c.running
											? 'Running: starts again after the rename.'
											: 'Stopped: stays stopped.'}
										Uses {c.volumes.join(', ')}.</span
									>
								</li>
							{/each}
						</ul>
					{/if}
				</section>
			{/if}
		</div>
	{/snippet}
</DestructiveConfirm>

<style>
	.form {
		display: grid;
		gap: var(--space-3);
		margin-top: var(--space-4);
	}

	.row {
		display: grid;
		grid-template-columns: 1fr auto;
		align-items: end;
		gap: var(--space-2);
	}

	.plan {
		display: grid;
		gap: var(--space-1);
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-canvas);
	}

	h3 {
		margin-top: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: 500;
	}

	h3:first-child {
		margin-top: 0;
	}

	.items,
	.issues {
		display: grid;
		gap: 4px;
		margin: 0;
	}

	.issues {
		padding-left: 18px;
	}

	.items li {
		display: grid;
		gap: 2px;
	}

	.mono {
		color: var(--text-strong);
		font-family: var(--font-mono);
		font-size: 12.5px;
	}

	.muted {
		color: var(--text-muted);
	}
</style>
