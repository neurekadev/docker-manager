<script lang="ts">
	// Rename a stack's Compose project in place (#7), in the stack header's
	// title row: the field edits the project name (labelled "Stack Name",
	// also when the heading shows a display name). Enter or the check button
	// renames at once, without a confirmation (owner decision); Escape or
	// the cancel button leaves the name as it was. The name is checked
	// first like the server does (`renameNameError`), then the server's
	// rename preview is asked silently for what would refuse it (a name
	// already taken, a Compose file whose `name:` fixes the project name:
	// `renameRefusal`), shown under the field. Nothing else is previewed:
	// the stack.rename job starts (If-Match) and is tracked in the page's
	// job tray with kind stack.rename, so the header shows the stack as
	// being renamed until it ends.
	import { useQueryClient } from '@tanstack/svelte-query';
	import Check from '@lucide/svelte/icons/check';
	import X from '@lucide/svelte/icons/x';
	import { IconButton, TextField, errorMessage } from '$lib/ui';
	import { previewRename, renameStack } from './actions';
	import { stackKeys, type Stack } from './queries';
	import { renameNameError, renameRefusal } from './rename';
	import type { JobTray } from './tray.svelte';

	interface Props {
		stack: Stack;
		tray: JobTray;
		/** Leaves the editor (cancelled, or the rename started). */
		onclose: () => void;
	}

	let { stack, tray, onclose }: Props = $props();
	const queryClient = useQueryClient();

	// The editor opens with the project name (not the display name).
	// svelte-ignore state_referenced_locally
	let name = $state(stack.name);
	let error = $state<string | undefined>();
	let submitting = $state(false);
	let input = $state<HTMLInputElement | null>(null);

	// Focus the field with the name selected once it is there.
	let focused = false;
	$effect(() => {
		if (!input || focused) return;
		focused = true;
		input.focus();
		input.select();
	});

	// The heading may show a display name; the field edits the project name.
	const note = $derived(
		stack.displayName && stack.displayName !== stack.name
			? `the display name ${stack.displayName} stays`
			: undefined
	);

	async function submit(e?: SubmitEvent) {
		e?.preventDefault();
		if (submitting) return;
		const from = stack.name;
		const to = name.trim();
		error = renameNameError(to, from);
		if (error) return;
		submitting = true;
		try {
			const refused = renameRefusal(to, from, await previewRename(stack.id, to));
			if (refused) {
				error = refused;
				return;
			}
			const job = await renameStack(stack, to);
			tray.add(job, {
				kind: 'stack.rename',
				title: `Rename ${from} to ${to}`,
				success: `Renamed ${from} to ${to}`,
				failure: `${from} was not renamed`,
				onfinish: () => {
					void queryClient.invalidateQueries({ queryKey: stackKeys.all });
					void queryClient.invalidateQueries({ queryKey: stackKeys.jobs(stack.id) });
				}
			});
			void queryClient.invalidateQueries({ queryKey: stackKeys.detail(stack.id) });
			void queryClient.invalidateQueries({ queryKey: stackKeys.jobs(stack.id) });
			onclose();
		} catch (err) {
			error = `${from} was not renamed: ${errorMessage(err)}`;
		} finally {
			submitting = false;
		}
	}
</script>

<form class="rename" onsubmit={submit} novalidate aria-label="Rename {stack.name}">
	<!-- The visible hint; the field's own label says the same to assistive technology. -->
	<span class="hint" aria-hidden="true"
		>Stack Name{#if note}<span class="note"> · {note}</span>{/if}</span
	>
	<div class="row">
		<div class="field">
			<TextField
				label="Stack Name"
				hideLabel
				bind:value={name}
				bind:ref={input}
				{error}
				mono
				required
				autocomplete="off"
				spellcheck="false"
				maxlength={63}
				readonly={submitting}
				oninput={() => (error = undefined)}
				onkeydown={(e) => {
					if (e.key === 'Escape') {
						e.preventDefault();
						e.stopPropagation();
						onclose();
					}
				}}
			/>
		</div>
		<IconButton
			type="submit"
			variant="secondary"
			icon={Check}
			label="Rename Stack"
			disabled={submitting}
			aria-busy={submitting || undefined}
		/>
		<IconButton
			variant="ghost"
			icon={X}
			label="Cancel Rename"
			disabled={submitting}
			onclick={onclose}
		/>
	</div>
</form>

<style>
	.rename {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
		flex: 1 1 320px;
		min-width: 0;
		max-width: 480px;
	}

	.hint {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.row {
		display: flex;
		align-items: flex-start;
		gap: var(--space-1);
	}

	.field {
		flex: 1;
		min-width: 0;
	}
</style>
