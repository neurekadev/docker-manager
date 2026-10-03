<script lang="ts">
	// "Compare" of the external-change conflict (#15): the version on disk
	// now against the unsaved buffer, as a unified line diff.
	import { Button, Dialog } from '$lib/ui';
	import { diffLines, diffRows } from './diff';

	interface Props {
		open?: boolean;
		name: string;
		disk: string;
		buffer: string;
	}

	let { open = $bindable(false), name, disk, buffer }: Props = $props();
	const diff = $derived(open ? diffLines(disk, buffer) : null);
	const rows = $derived(diff ? diffRows(diff.ops) : []);
</script>

<Dialog
	bind:open
	title="Compare {name}"
	description="Lines marked − are on disk now; lines marked + are your unsaved edits."
	size="lg"
>
	{#if diff && rows.length === 0}
		<p class="note">Your edits and the version on disk are identical.</p>
	{:else if diff}
		<p class="summary num">
			<span class="add">+{diff.added}</span> <span class="remove">−{diff.removed}</span>
		</p>
		<div class="diff mono" role="table" aria-label="Differences in {name}">
			{#each rows as r, i (i)}
				<div class="line {r.type}" role="row">
					<span class="no" role="cell">{r.a ?? ''}</span>
					<span class="no" role="cell">{r.b ?? ''}</span>
					<span
						class="mark"
						role="cell"
						aria-label={r.type === 'add'
							? 'Added'
							: r.type === 'remove'
								? 'Removed'
								: undefined}
						>{r.type === 'add' ? '+' : r.type === 'remove' ? '−' : ''}</span
					>
					<span class="text" role="cell">{r.text}</span>
				</div>
			{/each}
		</div>
	{/if}
	{#snippet footer()}
		<Button variant="secondary" onclick={() => (open = false)}>Close</Button>
	{/snippet}
</Dialog>

<style>
	.note {
		color: var(--text-default);
	}

	.summary {
		margin-bottom: var(--space-2);
		font-size: var(--text-caption);
	}

	.add {
		color: var(--ok);
	}

	.remove {
		color: var(--danger);
	}

	.diff {
		max-height: min(60dvh, 560px);
		overflow: auto;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
		font-size: 12.5px;
		line-height: 20px;
	}

	.line {
		display: grid;
		grid-template-columns: 44px 44px 20px max-content;
		white-space: pre;
	}

	.no {
		padding-right: var(--space-2);
		color: var(--text-muted);
		text-align: right;
		user-select: none;
	}

	.mark {
		text-align: center;
		user-select: none;
	}

	.line.add {
		background: var(--ok-soft);
	}

	.line.add .mark {
		color: var(--ok);
	}

	.line.remove {
		background: var(--danger-soft);
	}

	.line.remove .mark {
		color: var(--danger);
	}

	.line.skip {
		color: var(--text-muted);
		background: var(--surface-raised);
		font-family: var(--font-sans);
		font-size: var(--text-caption);
	}

	.line.skip .text {
		padding: 0 var(--space-2);
	}
</style>
