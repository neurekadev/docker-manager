<script lang="ts">
	// ⌘K command palette (#22): jump to pages and to anything the permission-
	// filtered GET /api/v1/search finds (within the selected environment when
	// one is selected). A combobox + listbox: arrows move, Enter opens,
	// Escape closes and focus returns to where it was.
	import { createQuery } from '@tanstack/svelte-query';
	import { Dialog } from 'bits-ui';
	import CornerDownLeft from '@lucide/svelte/icons/corner-down-left';
	import Search from '@lucide/svelte/icons/search';
	import { searchQuery } from '$lib/api/queries';
	import Spinner from '$lib/ui/Spinner.svelte';
	import { grouped, hitResults, pageResults, type PaletteResult } from './palette';
	import type { NavItem } from './nav';

	interface Props {
		open?: boolean;
		pages: NavItem[];
		environmentId: string | null;
		environmentName?: string | null;
		onnavigate: (href: string) => void;
		debounce?: number;
	}

	let {
		open = $bindable(false),
		pages,
		environmentId,
		environmentName,
		onnavigate,
		debounce = 150
	}: Props = $props();

	const uid = $props.id();
	let q = $state('');
	let dq = $state('');
	let active = $state(0);
	let returnTo: HTMLElement | null = null;

	$effect.pre(() => {
		if (open && typeof document !== 'undefined') {
			returnTo =
				document.activeElement instanceof HTMLElement ? document.activeElement : null;
		}
	});

	$effect(() => {
		const next = q.trim();
		const t = setTimeout(() => (dq = next), debounce);
		return () => clearTimeout(t);
	});

	$effect(() => {
		if (!open) {
			q = '';
			dq = '';
		}
	});

	const search = createQuery(() => ({
		...searchQuery(dq, environmentId),
		enabled: open && dq.length > 0
	}));

	const results = $derived<PaletteResult[]>([
		...pageResults(pages, q).slice(0, q ? 5 : pages.length),
		...(dq && dq === q.trim() ? hitResults(search.data) : [])
	]);
	const groups = $derived(grouped(results));
	const gaps = $derived(dq && search.data ? search.data.gaps : []);

	$effect(() => {
		// Reset the highlight when the result list changes.
		void results.length;
		active = 0;
	});

	function go(r: PaletteResult | undefined) {
		if (!r) return;
		open = false;
		onnavigate(r.href);
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') {
			active = Math.min(results.length - 1, active + 1);
		} else if (e.key === 'ArrowUp') {
			active = Math.max(0, active - 1);
		} else if (e.key === 'Home') {
			active = 0;
		} else if (e.key === 'End') {
			active = results.length - 1;
		} else if (e.key === 'Enter') {
			go(results[active]);
		} else {
			return;
		}
		e.preventDefault();
		document.getElementById(`${uid}-opt-${active}`)?.scrollIntoView({ block: 'nearest' });
	}

	const optionIndex = (r: PaletteResult) => results.indexOf(r);
</script>

<Dialog.Root bind:open>
	<Dialog.Portal>
		<Dialog.Overlay class="dy-overlay" />
		<Dialog.Content
			class="palette"
			onCloseAutoFocus={(e) => {
				if (returnTo?.isConnected) {
					e.preventDefault();
					returnTo.focus();
				}
			}}
		>
			<Dialog.Title class="sr-only">Search Docker Manager</Dialog.Title>
			<div class="input-row">
				<Search size={18} strokeWidth={1.75} aria-hidden="true" />
				<input
					bind:value={q}
					role="combobox"
					aria-expanded="true"
					aria-controls="{uid}-list"
					aria-activedescendant={results.length ? `${uid}-opt-${active}` : undefined}
					aria-autocomplete="list"
					aria-label="Search pages, environments, stacks and containers"
					placeholder={environmentName
						? `Search in ${environmentName}…`
						: 'Search anything…'}
					autocomplete="off"
					spellcheck="false"
					onkeydown={onKey}
				/>
				{#if search.isFetching}<Spinner size={16} />{/if}
			</div>
			<div class="results" id="{uid}-list" role="listbox" aria-label="Results">
				{#each groups as g (g.group)}
					<div role="group" aria-labelledby="{uid}-g-{g.group}">
						<div class="group" id="{uid}-g-{g.group}" role="presentation">
							{g.group}
						</div>
						{#each g.items as r (r.id)}
							{@const i = optionIndex(r)}
							{@const Icon = r.icon}
							<div
								id="{uid}-opt-{i}"
								role="option"
								aria-selected={i === active}
								class="option"
								class:active={i === active}
								tabindex="-1"
								onclick={() => go(r)}
								onkeydown={(e) => e.key === 'Enter' && go(r)}
								onpointermove={() => (active = i)}
							>
								<Icon size={16} strokeWidth={1.75} aria-hidden="true" />
								<span class="label">{r.label}</span>
								{#if r.secondary}<span class="secondary">{r.secondary}</span>{/if}
								{#if i === active}<CornerDownLeft
										size={14}
										strokeWidth={1.75}
										aria-hidden="true"
										class="enter"
									/>{/if}
							</div>
						{/each}
					</div>
				{/each}
				{#if q && dq === q.trim() && !search.isFetching && results.length === 0}
					<p class="empty">Nothing found for “{q}”.</p>
				{/if}
			</div>
			{#if gaps.length}
				<p class="gaps" role="status">
					Not searched: {gaps
						.map(
							(g) =>
								`${g.environmentName} (${g.reason === 'offline' ? 'offline' : 'not answering'})`
						)
						.join(', ')}.
				</p>
			{/if}
			<p class="hint" aria-hidden="true">↑↓ to move, Enter to open, Esc to close</p>
		</Dialog.Content>
	</Dialog.Portal>
</Dialog.Root>

<style>
	:global(.palette) {
		position: fixed;
		top: min(16vh, 120px);
		left: 50%;
		z-index: var(--z-dialog);
		display: flex;
		flex-direction: column;
		width: min(640px, calc(100vw - 24px));
		max-height: min(560px, calc(100dvh - 140px));
		transform: translateX(-50%);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
		box-shadow: var(--shadow-float);
		outline: none;
		overflow: hidden;
	}

	/* The palette has only a max-height, so overflowing results would
	   otherwise shrink the input row and the footers along with them. */
	.input-row,
	.gaps,
	.hint {
		flex-shrink: 0;
	}

	.input-row {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		padding: 0 var(--space-4);
		height: 52px;
		border-bottom: 1px solid var(--border-subtle);
		color: var(--text-muted);
	}

	input {
		flex: 1;
		min-width: 0;
		height: 100%;
		border: 0;
		background: none;
		color: var(--text-strong);
		font-size: 15px;
		outline: none;
	}

	input::placeholder {
		color: var(--text-muted);
	}

	.results {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		padding: var(--space-2);
	}

	.group {
		padding: var(--space-2) var(--space-2) 4px;
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
	}

	.option {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-height: 36px;
		padding: 0 var(--space-2);
		border-radius: var(--radius-sm);
		color: var(--text-default);
		cursor: pointer;
	}

	.option.active {
		background: var(--surface-selected);
		color: var(--text-strong);
	}

	.label {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.secondary {
		color: var(--text-muted);
		font-size: var(--text-caption);
		white-space: nowrap;
	}

	.option :global(.enter) {
		margin-left: auto;
		color: var(--text-muted);
	}

	.empty,
	.gaps {
		padding: var(--space-3) var(--space-3);
		color: var(--text-muted);
	}

	.gaps {
		border-top: 1px solid var(--border-subtle);
		font-size: var(--text-caption);
	}

	.hint {
		padding: 6px var(--space-4);
		border-top: 1px solid var(--border-subtle);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	@media (max-width: 767px) {
		:global(.palette) {
			top: 0;
			width: 100vw;
			max-height: 100dvh;
			height: 100dvh;
			border-radius: 0;
		}

		.hint {
			display: none;
		}
	}
</style>
