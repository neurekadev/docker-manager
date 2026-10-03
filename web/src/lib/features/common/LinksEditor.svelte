<script lang="ts">
	// Links editor (stack details, template details): a repeatable list of
	// Label (optional) and URL rows, a grip handle to reorder them (drag,
	// or arrow keys on the handle: Sortable), a remove button on each and
	// "Add Link" (at most 10). The server's rules are checked inline
	// (`linkProblems`): a field shows its problem once it was left or the
	// form was submitted (`showAll`); the server's answer to the last save
	// (`serverProblems`, from `serverLinkProblems`) shows until that row
	// changes and follows its row when rows move. The parent saves
	// `cleanLinks(rows)` in the order shown; rows come from `linkRows`.
	import Plus from '@lucide/svelte/icons/plus';
	import X from '@lucide/svelte/icons/x';
	import { untrack } from 'svelte';
	import { Button, DragHandle, IconButton, Sortable, TextField, moveItem } from '$lib/ui';
	import {
		MAX_LINKS,
		linkProblems,
		newLinkRow,
		type LinkRow,
		type LinkRowProblem
	} from './links';

	interface Props {
		rows: LinkRow[];
		/** Show every problem (after a save attempt). */
		showAll?: boolean;
		/** Problems the server reported for the last save. */
		serverProblems?: { rows: LinkRowProblem[]; list: string | null } | null;
		disabled?: boolean;
	}

	let {
		rows = $bindable([]),
		showAll = false,
		serverProblems = null,
		disabled = false
	}: Props = $props();

	const uid = $props.id();
	const keyOf = (r: LinkRow, i: number) => r.key ?? -1 - i;
	// Fields the user left, and rows changed since the server answered.
	let touched = $state<Record<string, boolean>>({});
	let edited = $state<Record<number, boolean>>({});
	// The rows' keys when the server answered: its problems name positions.
	let serverKeys = $state.raw<number[]>([]);
	$effect.pre(() => {
		void serverProblems;
		edited = {};
		serverKeys = untrack(() => rows.map(keyOf));
	});

	const sort = new Sortable({ onmove: (from, to) => (rows = moveItem(rows, from, to)) });

	const problems = $derived(linkProblems(rows));

	function problem(i: number, field: 'label' | 'url'): string | null {
		const k = keyOf(rows[i], i);
		const at = serverKeys.indexOf(k);
		const server = edited[k] || at < 0 ? undefined : serverProblems?.rows[at]?.[field];
		if (server) return server;
		if (!showAll && !touched[`${k}:${field}`]) return null;
		return problems.rows[i]?.[field] ?? null;
	}

	const listProblem = $derived(
		serverProblems?.list && !Object.keys(edited).length ? serverProblems.list : problems.list
	);
</script>

<fieldset class="links" aria-describedby="links-{uid}-desc">
	<legend>Links</legend>
	<p class="desc" id="links-{uid}-desc">
		Optional. Pages such as the documentation, website or repository, shown on the page. Each
		opens in a new tab.
	</p>
	{#if rows.length}
		<div class="head" aria-hidden="true">
			<span></span>
			<span>Label</span>
			<span>URL</span>
		</div>
		<ul class="rows">
			{#each rows as row, i (keyOf(row, i))}
				{@const k = keyOf(row, i)}
				<li class="row" {@attach sort.item(i)}>
					<div class="grip">
						<DragHandle
							sortable={sort}
							index={i}
							name="Link {i + 1}"
							disabled={disabled || rows.length < 2}
						/>
					</div>
					<TextField
						label="Label of Link {i + 1}"
						hideLabel
						bind:value={row.label}
						placeholder="Documentation"
						maxlength={120}
						autocomplete="off"
						{disabled}
						error={problem(i, 'label')}
						oninput={() => (edited[k] = true)}
						onblur={() => (touched[`${k}:label`] = true)}
					/>
					<TextField
						label="URL of Link {i + 1}"
						hideLabel
						inputmode="url"
						bind:value={row.url}
						placeholder="https://"
						maxlength={4096}
						autocomplete="off"
						spellcheck={false}
						{disabled}
						error={problem(i, 'url')}
						oninput={() => (edited[k] = true)}
						onblur={() => (touched[`${k}:url`] = true)}
					/>
					<div class="remove">
						<IconButton
							label="Remove Link {i + 1}"
							icon={X}
							{disabled}
							onclick={() => rows.splice(i, 1)}
						/>
					</div>
				</li>
			{/each}
		</ul>
	{/if}
	{#if listProblem}<p class="error" role="alert">{listProblem}</p>{/if}
	{#if rows.length < MAX_LINKS}
		<div>
			<Button size="sm" icon={Plus} {disabled} onclick={() => rows.push(newLinkRow())}
				>Add Link</Button
			>
		</div>
	{/if}
</fieldset>

<style>
	.links {
		display: grid;
		gap: var(--space-2);
		min-width: 0;
		margin: 0;
		padding: 0;
		border: 0;
	}

	legend {
		padding: 0;
		margin-bottom: var(--space-1);
		color: var(--text-default);
		font-weight: var(--weight-medium);
	}

	.desc {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.head,
	.row {
		display: grid;
		grid-template-columns: auto minmax(120px, 2fr) minmax(0, 3fr) auto;
		align-items: start;
		gap: var(--space-2);
	}

	.head {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.row {
		border-radius: var(--radius-sm);
	}

	.rows {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.error {
		color: var(--danger);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	@media (max-width: 767px) {
		.head {
			display: none;
		}

		/* Label and URL stack between the grip and the remove button. */
		.row {
			grid-template-columns: auto minmax(0, 1fr) auto;
		}

		.row > :global(*) {
			grid-column: 2;
		}

		.row > .grip {
			grid-column: 1;
			grid-row: 1;
		}

		.row > .remove {
			grid-column: 3;
			grid-row: 1;
		}
	}
</style>
