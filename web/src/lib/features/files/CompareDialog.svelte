<script lang="ts">
	// "Compare" of the external-change conflict (#15): the version on disk
	// now against the unsaved buffer, as a unified line diff. Rows have a
	// fixed height and render windowed, so a huge comparison stays smooth.
	import { Button, Dialog } from '$lib/ui';
	import { virtualWindow } from '$lib/ui/table';
	import { diffLines, diffRows } from './diff';

	const ROW = 20;

	interface Props {
		open?: boolean;
		name: string;
		disk: string;
		buffer: string;
	}

	let { open = $bindable(false), name, disk, buffer }: Props = $props();
	const diff = $derived(open ? diffLines(disk, buffer) : null);
	const rows = $derived(diff ? diffRows(diff.ops) : []);

	let scroller = $state<HTMLElement>();
	let scrollTop = $state(0);
	let viewport = $state(560);
	// A reopened dialog starts at the top again.
	$effect(() => {
		if (scroller) scrollTop = scroller.scrollTop;
	});
	const win = $derived(virtualWindow(scrollTop, viewport, ROW, rows.length, 20));
	const shown = $derived(rows.slice(win.start, win.end));
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
		<div
			class="diff mono"
			role="table"
			aria-label="Differences in {name}"
			aria-rowcount={rows.length}
			bind:this={scroller}
			bind:clientHeight={viewport}
			onscroll={() => scroller && (scrollTop = scroller.scrollTop)}
		>
			<div style="height: {win.padTop}px" aria-hidden="true"></div>
			{#each shown as r, i (win.start + i)}
				<div class="line {r.type}" role="row" aria-rowindex={win.start + i + 1}>
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
			<div style="height: {win.padBottom}px" aria-hidden="true"></div>
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
		height: 20px;
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
