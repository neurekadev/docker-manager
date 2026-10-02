<script lang="ts">
	// Diff view (#22 revision compare, #7): a unified line diff of one file
	// with old and new line numbers, +/- markers (never colour alone) and
	// collapsed unchanged regions that can be expanded. Logic in diff.ts.
	import ChevronsUpDown from '@lucide/svelte/icons/chevrons-up-down';
	import { diffText, type DiffLine } from './diff';

	interface Props {
		/** File name shown in the header (also the region's name). */
		title: string;
		before: string;
		after: string;
		/** What the two sides are, e.g. "Revision 3" and "On disk". */
		beforeLabel?: string;
		afterLabel?: string;
		context?: number;
	}

	let {
		title,
		before,
		after,
		beforeLabel = 'Before',
		afterLabel = 'After',
		context = 3
	}: Props = $props();

	let expanded = $state(false);
	const result = $derived(diffText(before, after, expanded ? Number.MAX_SAFE_INTEGER : context));

	const marker = (l: DiffLine) => (l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' ');
	const srKind = (l: DiffLine) =>
		l.kind === 'add' ? 'Added: ' : l.kind === 'del' ? 'Removed: ' : '';
</script>

<section class="diff" aria-label="Changes in {title}">
	<header class="head">
		<span class="file mono">{title}</span>
		<span class="stats num">
			{#if result.same}
				<span class="muted">No Changes</span>
			{:else}
				<span class="add">+{result.added}</span>
				<span class="del">−{result.removed}</span>
				<span class="sr-only">
					{result.added} lines added, {result.removed} lines removed ({beforeLabel} to {afterLabel})
				</span>
			{/if}
		</span>
	</header>
	{#if !result.same}
		<div class="scroll">
			<table class="lines">
				<thead class="sr-only">
					<tr><th>{beforeLabel} Line</th><th>{afterLabel} Line</th><th>Change</th></tr>
				</thead>
				<tbody>
					{#each result.hunks as hunk, hi (hi)}
						{#if hunk.skippedBefore > 0}
							<tr class="skip">
								<td colspan="3">
									<button
										type="button"
										class="expand"
										onclick={() => (expanded = true)}
									>
										<ChevronsUpDown
											size={14}
											strokeWidth={1.75}
											aria-hidden="true"
										/>
										Show {hunk.skippedBefore} Unchanged {hunk.skippedBefore ===
										1
											? 'Line'
											: 'Lines'}
									</button>
								</td>
							</tr>
						{/if}
						{#each hunk.lines as line, li (li)}
							<tr class={line.kind}>
								<td class="no num">{line.oldNo ?? ''}</td>
								<td class="no num">{line.newNo ?? ''}</td>
								<td class="code mono"
									><span class="mark" aria-hidden="true">{marker(line)}</span
									><span class="sr-only">{srKind(line)}</span>{line.text ||
										' '}</td
								>
							</tr>
						{/each}
					{/each}
					{#if result.skippedAfter > 0}
						<tr class="skip">
							<td colspan="3">
								<button
									type="button"
									class="expand"
									onclick={() => (expanded = true)}
								>
									<ChevronsUpDown
										size={14}
										strokeWidth={1.75}
										aria-hidden="true"
									/>
									Show {result.skippedAfter} Unchanged {result.skippedAfter === 1
										? 'Line'
										: 'Lines'}
								</button>
							</td>
						</tr>
					{/if}
				</tbody>
			</table>
		</div>
		{#if expanded}
			<button type="button" class="collapse" onclick={() => (expanded = false)}
				>Hide Unchanged Lines</button
			>
		{/if}
	{/if}
</section>

<style>
	.diff {
		min-width: 0;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
		overflow: hidden;
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-3);
		border-bottom: 1px solid var(--border-subtle);
		background: var(--surface-raised);
	}

	.file {
		color: var(--text-strong);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.stats {
		display: inline-flex;
		gap: var(--space-2);
		font-size: var(--text-caption);
	}

	.add {
		color: var(--ok);
	}

	.del {
		color: var(--danger);
	}

	.scroll {
		overflow-x: auto;
	}

	.lines {
		width: 100%;
		border-collapse: collapse;
		font-size: 12.5px;
		line-height: 20px;
	}

	.no {
		width: 1%;
		padding: 0 var(--space-2);
		color: var(--code-gutter);
		text-align: right;
		user-select: none;
		white-space: nowrap;
	}

	.code {
		padding: 0 var(--space-3) 0 var(--space-1);
		color: var(--text-default);
		white-space: pre;
	}

	.mark {
		display: inline-block;
		width: 1.25em;
		color: var(--text-muted);
		user-select: none;
	}

	tr.add {
		background: color-mix(in srgb, var(--ok-soft) 80%, transparent);
	}

	tr.add .mark {
		color: var(--ok);
	}

	tr.del {
		background: color-mix(in srgb, var(--danger-soft) 70%, transparent);
	}

	tr.del .mark {
		color: var(--danger);
	}

	.skip td {
		padding: 0;
		border-block: 1px solid var(--border-subtle);
		background: var(--surface-panel);
	}

	.expand,
	.collapse {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		width: 100%;
		min-height: 28px;
		padding: 2px var(--space-3);
		border: 0;
		background: transparent;
		color: var(--accent-text);
		font: inherit;
		font-size: var(--text-caption);
		cursor: pointer;
	}

	.expand:hover,
	.collapse:hover {
		background: var(--surface-hover);
	}

	.collapse {
		border-top: 1px solid var(--border-subtle);
	}
</style>
