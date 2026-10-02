<script lang="ts">
	// Log viewer (#8, #22 the mockup's log panel): container or stack
	// service logs merged by time, each service in its hue (the service hue
	// identity), the Services filter, Follow (live stream with cursor
	// resume), timestamps, line wrap, a search that shows only the matching
	// lines as you type (highlighted; Match case, regular expressions), the
	// Levels filter (each line's level and output stream, with counts),
	// clear view, download and pop-out. Unwrapped lines render in a
	// fixed-height window, so thousands of lines stay fast (wrapped lines
	// render all, skipping the off-screen ones' layout). Log lines are never
	// stored beyond the page.
	import { onDestroy, untrack } from 'svelte';
	import ArrowDownToLine from '@lucide/svelte/icons/arrow-down-to-line';
	import Box from '@lucide/svelte/icons/box';
	import CaseSensitive from '@lucide/svelte/icons/case-sensitive';
	import Clock from '@lucide/svelte/icons/clock';
	import Download from '@lucide/svelte/icons/download';
	import Eraser from '@lucide/svelte/icons/eraser';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import ListFilter from '@lucide/svelte/icons/list-filter';
	import Regex from '@lucide/svelte/icons/regex';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import TextWrap from '@lucide/svelte/icons/text-wrap';
	import type { Snippet } from 'svelte';
	import { SERVICE_HEX, TILE_HEX } from '$lib/design/hue';
	import {
		Button,
		EmptyState,
		IconButton,
		MultiSelect,
		Notice,
		Skeleton,
		Switch,
		TextField,
		toast,
		type MultiSelectGroup
	} from '$lib/ui';
	import { virtualWindow } from '$lib/ui/table';
	import { endReason, type LogFeed, type LogLine } from './feed.svelte';
	import {
		filterLines,
		formatLogTime,
		hiddenServices,
		highlight,
		levelSummary,
		logText,
		plainSearch,
		regexPattern,
		serviceShown,
		serviceSummary,
		serviceTally,
		tally
	} from './format';
	import { LOG_LEVELS, LOG_STREAMS } from './level';
	import { RegexSearch } from './regex-search.svelte';

	interface Props {
		feed: LogFeed;
		/** Accessible name of the log region, e.g. "Logs of Silo". */
		label: string;
		/** Stack logs: service hues and the Services filter. */
		stackId?: string;
		/** File name stem of downloads. */
		downloadName: string;
		onpopout?: () => void;
		/** Extra controls at the end of the toolbar (the dock's close button). */
		extra?: Snippet;
		dense?: boolean;
		/** Stack logs: start with only this service selected (?service=<name>). */
		service?: string | null;
	}

	let {
		feed,
		label,
		stackId,
		downloadName,
		onpopout,
		extra,
		dense = false,
		service = null
	}: Props = $props();

	const ROW = 20;
	let timestamps = $state(true);
	let wrap = $state(false);
	let query = $state('');
	let caseSensitive = $state(false);
	let regex = $state(false);
	// The Levels filter: the chosen levels and output streams (all at first).
	const EVERY: string[] = [...LOG_LEVELS, ...LOG_STREAMS].map((o) => o.value);
	let picked = $state<string[]>(EVERY);
	const levels = $derived(LOG_LEVELS.map((l) => l.value).filter((v) => picked.includes(v)));
	const streams = $derived(LOG_STREAMS.map((s) => s.value).filter((v) => picked.includes(v)));
	const narrowed = $derived(picked.length < EVERY.length);
	let hidden = $state<string[]>([]);
	let only = $state<string | null>(null);
	// The page's ?service=<name> selects one service (again when it changes).
	$effect.pre(() => {
		only = service || null;
		hidden = [];
	});

	const services = $derived.by(() => {
		const out: { service: string; keys: string[] }[] = [];
		for (const s of feed.sources) {
			const name = s.service ?? s.label;
			const g = out.find((x) => x.service === name);
			if (g) g.keys.push(s.key);
			else out.push({ service: name, keys: [s.key] });
		}
		return out;
	});
	const multi = $derived(feed.sources.length > 1 || !!stackId);
	const serviceOf = $derived(
		Object.fromEntries(feed.sources.map((s) => [s.key, s.service ?? s.label]))
	);
	const names = $derived(services.map((s) => s.service));
	const serviceState = $derived({ only, hidden, services: names });
	const isShown = (svc: string) => serviceShown(svc, serviceState);
	const shownServices = $derived(names.filter(isShown));
	const allShown = $derived(shownServices.length === names.length);
	// The search, then the services, then the levels. Each count tells how
	// many of the searched lines that choice shows with the other filters'
	// choices: a service's under the chosen levels (hidden ones too), the
	// levels under the chosen services and output, the output under the
	// chosen services and levels.
	const source = $derived(allShown ? undefined : (key: string) => isShown(serviceOf[key] ?? ''));
	const bySource = $derived(filterLines(feed.lines, { source }));
	// Plain text matches here; a regular expression runs in the search
	// worker, which stops it when it runs too long.
	const regexSearch = new RegexSearch(
		() => new Worker(new URL('./regex.worker.ts', import.meta.url), { type: 'module' })
	);
	onDestroy(() => regexSearch.dispose());
	const pattern = $derived(regex ? regexPattern(query, caseSensitive) : null);
	$effect(() => {
		const p = pattern;
		const lines = feed.lines;
		if (p && p !== 'invalid') untrack(() => regexSearch.run(p, lines));
	});
	// An invalid or too slow expression filters nothing until it is fixed;
	// so does any expression when the search worker failed.
	const refused = $derived(
		pattern === 'invalid'
			? 'invalid'
			: !pattern
				? null
				: regexSearch.failed
					? 'failed'
					: regexSearch.slow
						? 'slow'
						: null
	);
	const matcher = $derived.by(() => {
		if (!regex) return plainSearch(query, caseSensitive);
		if (!pattern || refused) return null;
		void regexSearch.version;
		return regexSearch.matcher();
	});
	const searchedAll = $derived(filterLines(feed.lines, { match: matcher }));
	const searched = $derived(filterLines(searchedAll, { source }));
	const serviceCounts = $derived(
		serviceTally(filterLines(searchedAll, { levels, streams }), serviceOf)
	);
	const serviceGroups = $derived<MultiSelectGroup[]>([
		{
			options: services.map((s) => ({
				value: s.service,
				label: s.service,
				hue: hueOf(s.service),
				count: serviceCounts[s.service] ?? 0
			}))
		}
	]);
	const shown = $derived(filterLines(searched, { levels, streams }));
	const levelCounts = $derived(tally(filterLines(searched, { streams })));
	const streamCounts = $derived(tally(filterLines(searched, { levels })));
	const levelGroups = $derived<MultiSelectGroup[]>([
		{
			label: 'Levels',
			options: LOG_LEVELS.map((l) => ({ ...l, count: levelCounts[l.value] }))
		},
		{
			label: 'Output',
			options: LOG_STREAMS.map((s) => ({
				value: s.value,
				label: s.label,
				count: streamCounts[s.value]
			}))
		}
	]);

	function onSearchKey(e: KeyboardEvent) {
		// Escape clears the search first (and leaves the drawer open).
		if (e.key === 'Escape' && query) {
			e.preventDefault();
			e.stopPropagation();
			query = '';
		}
	}

	/** A service's colour: its source's tile colour, else the one service colour (#22). */
	function hueOf(svc: string): string {
		const color = feed.sources.find((s) => (s.service ?? s.label) === svc)?.color;
		return color ? TILE_HEX[color].fg : SERVICE_HEX;
	}

	function pickServices(shown: string[]) {
		hidden = hiddenServices(names, shown);
		only = null;
	}

	// Windowed rendering and follow-scrolling.
	let scroller = $state<HTMLElement>();
	let scrollTop = $state(0);
	let viewport = $state(400);
	let atBottom = $state(true);
	// Wrapped lines have no fixed height: they render all (the buffer keeps
	// at most 5000) and the browser skips laying out the off-screen ones.
	const win = $derived(
		wrap
			? { start: 0, end: shown.length, padTop: 0, padBottom: 0 }
			: virtualWindow(scrollTop, viewport, ROW, shown.length, 20)
	);
	const rows = $derived(shown.slice(win.start, win.end));

	function onScroll() {
		if (!scroller) return;
		scrollTop = scroller.scrollTop;
		viewport = scroller.clientHeight;
		atBottom = scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - ROW * 2;
	}

	function toBottom() {
		if (!scroller) return;
		scroller.scrollTop = scroller.scrollHeight;
		onScroll();
	}

	$effect(() => {
		if (!scroller) return;
		viewport = scroller.clientHeight;
		const ro = new ResizeObserver(() => scroller && (viewport = scroller.clientHeight));
		ro.observe(scroller);
		return () => ro.disconnect();
	});

	// New lines while following and at the bottom: stay at the bottom.
	$effect(() => {
		void shown.length;
		if (feed.following && atBottom) queueMicrotask(toBottom);
	});

	const problems = $derived(
		Object.entries(feed.states).filter(([, s]) => s.kind === 'ended' || s.kind === 'failed')
	);
	const waiting = $derived(
		feed.sources.length > 0 &&
			Object.values(feed.states).every(
				(s) => s.kind === 'connecting' || s.kind === 'reconnecting'
			)
	);
	const liveCount = $derived(Object.values(feed.states).filter((s) => s.kind === 'live').length);
	const labelOf = (key: string) => feed.sources.find((s) => s.key === key)?.label ?? key;

	let downloadUrl: string | null = null;
	function download() {
		const text = logText(shown, {
			timestamps: true,
			source: multi ? (k) => labelOf(k) : undefined
		});
		if (downloadUrl) URL.revokeObjectURL(downloadUrl);
		downloadUrl = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
		const a = document.createElement('a');
		a.href = downloadUrl;
		a.download = `${downloadName}-${new Date().toISOString().replace(/[:.]/g, '-')}.log`;
		document.body.appendChild(a);
		a.click();
		a.remove();
		toast.success(`Downloaded ${shown.length} log lines`);
	}
	onDestroy(() => downloadUrl && URL.revokeObjectURL(downloadUrl));

	function lineLabel(l: LogLine): string | undefined {
		return l.stream === 'stderr' ? 'standard error' : undefined;
	}
</script>

<section class="viewer" class:dense aria-label={label}>
	<div class="toolbar" role="toolbar" aria-label="Log controls">
		{#if stackId && services.length > 1}
			<MultiSelect
				label="Services"
				hideLabel
				title="Services"
				icon={Box}
				groups={serviceGroups}
				summary={serviceSummary(names, shownServices)}
				value={shownServices}
				onchange={pickServices}
			/>
		{/if}
		<div class="search" role="search" aria-label="Search logs">
			<TextField
				label="Search logs"
				hideLabel
				type="search"
				placeholder={regex ? 'Search with a regular expression' : 'Search logs'}
				spellcheck="false"
				autocomplete="off"
				mono={regex}
				aria-invalid={!!refused || undefined}
				onkeydown={onSearchKey}
				bind:value={query}
			/>
			<IconButton
				icon={CaseSensitive}
				size="sm"
				label="Match case"
				pressed={caseSensitive}
				onclick={() => (caseSensitive = !caseSensitive)}
			/>
			<IconButton
				icon={Regex}
				size="sm"
				label="Use regular expression"
				pressed={regex}
				onclick={() => (regex = !regex)}
			/>
			<span class="count num" class:invalid={!!refused} aria-live="polite"
				>{#if refused === 'invalid'}Invalid expression{:else if refused === 'slow'}<span
						title="Searching took too long and was stopped. Simplify the expression, for example avoid repeats inside repeats such as (a+)+."
						>Too slow to search</span
					>{:else if refused === 'failed'}<span
						title="This expression could not be searched. Change it, or turn off Use regular expression to search plain text."
						>Search unavailable</span
					>{:else if matcher}{shown.length}
					{shown.length === 1 ? 'match' : 'matches'}{/if}</span
			>
		</div>
		<div class="filters">
			<MultiSelect
				label="Levels"
				hideLabel
				title="Levels and output"
				icon={ListFilter}
				groups={levelGroups}
				summary={levelSummary(levels, streams)}
				bind:value={picked}
			/>
			<div class="view" role="group" aria-label="Display">
				<IconButton
					icon={TextWrap}
					size="sm"
					label="Wrap lines"
					pressed={wrap}
					onclick={() => (wrap = !wrap)}
				/>
				<IconButton
					icon={Clock}
					size="sm"
					label="Timestamps"
					pressed={timestamps}
					onclick={() => (timestamps = !timestamps)}
				/>
			</div>
			<div class="follow">
				<Switch
					checked={feed.following}
					label="Follow"
					onchange={(on) => feed.setFollowing(on)}
				/>
			</div>
		</div>
		<div class="actions">
			<IconButton icon={Eraser} size="sm" label="Clear view" onclick={() => feed.clear()} />
			<IconButton
				icon={Download}
				size="sm"
				label="Download shown lines"
				disabled={shown.length === 0}
				onclick={download}
			/>
			{#if onpopout}
				<IconButton
					icon={ExternalLink}
					size="sm"
					label="Open in a new window"
					onclick={onpopout}
				/>
			{/if}
			{#if extra}{@render extra()}{/if}
		</div>
	</div>

	{#if feed.dropped > 0 || feed.trimmed > 0 || problems.length}
		<div class="notices">
			{#if feed.dropped > 0}
				<Notice tone="warn" live="none" title="Skipped {feed.dropped} lines">
					This view fell behind the log stream. Download the logs or show fewer services
					to keep up.
				</Notice>
			{/if}
			{#each problems as [key, st] (key)}
				<Notice
					tone={st.kind === 'failed' && st.status === 403 ? 'info' : 'warn'}
					live="none"
					title={st.kind === 'ended'
						? `${labelOf(key)}: ${endReason(st.reason)}`
						: `${labelOf(key)}: ${st.kind === 'failed' ? st.message : ''}`}
				>
					{#snippet actions()}
						{#if !(st.kind === 'failed' && st.status === 403)}
							<Button
								size="sm"
								variant="ghost"
								icon={RotateCw}
								onclick={() => feed.retry(key)}>Reconnect</Button
							>
						{/if}
					{/snippet}
				</Notice>
			{/each}
		</div>
	{/if}

	<!-- A scrollable region must be reachable by keyboard to scroll it (WCAG 2.1.1). -->
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div
		class="body mono"
		bind:this={scroller}
		onscroll={onScroll}
		role="log"
		aria-live="off"
		aria-label="{label} lines"
		tabindex="0"
	>
		{#if shown.length === 0}
			<div class="empty">
				{#if waiting || (regex && matcher && regexSearch.busy && searched.length === 0)}
					<div aria-busy="true"><Skeleton lines={5} /></div>
				{:else if feed.lines.length > 0 && !shownServices.length}
					<EmptyState
						compact
						icon={ScrollText}
						title="Every service is hidden"
						description="Choose a service in Services to show its lines."
					>
						{#snippet actions()}
							<Button size="sm" onclick={() => pickServices(names)}
								>Show all services</Button
							>
						{/snippet}
					</EmptyState>
				{:else if matcher && bySource.length > 0 && searched.length === 0}
					<EmptyState
						compact
						icon={ScrollText}
						title="No lines match “{query.trim()}”"
						description="Change the search to see more lines."
					>
						{#snippet actions()}
							<Button size="sm" onclick={() => (query = '')}>Clear search</Button>
						{/snippet}
					</EmptyState>
				{:else if searched.length > 0 && narrowed}
					<EmptyState
						compact
						icon={ScrollText}
						title="No lines at the chosen levels"
						description="Choose more in Levels to see more lines."
					>
						{#snippet actions()}
							<Button size="sm" onclick={() => (picked = EVERY)}
								>Show all levels</Button
							>
						{/snippet}
					</EmptyState>
				{:else if feed.lines.length > 0}
					<EmptyState
						compact
						icon={ScrollText}
						title="No lines from the selected services"
						description="Choose more in Services to see more lines."
					>
						{#snippet actions()}
							<Button size="sm" onclick={() => pickServices(names)}
								>Show all services</Button
							>
						{/snippet}
					</EmptyState>
				{:else}
					<EmptyState
						compact
						icon={ScrollText}
						title="No log lines yet"
						description={feed.following
							? 'New lines appear here as soon as they are written.'
							: 'Turn on Follow to stream new lines.'}
					/>
				{/if}
			</div>
		{:else}
			<div style="height: {win.padTop}px" aria-hidden="true"></div>
			{#each rows as l (l.seq)}
				<div
					class="line"
					class:wrap
					class:stderr={l.stream === 'stderr'}
					data-level={l.level}
					style={wrap ? undefined : `height: ${ROW}px`}
				>
					{#if timestamps}<span class="ts num">{formatLogTime(l.at)}</span>{/if}
					{#if multi}
						<span class="src" style="color: {hueOf(serviceOf[l.source] ?? '')}"
							>{labelOf(l.source)}</span
						>
						<span class="bar" aria-hidden="true">|</span>
					{/if}
					<span class="text" aria-label={lineLabel(l)}
						>{#each highlight(l.text, matcher ? matcher.ranges(l) : []) as part, i (i)}{#if part.match}<mark
									>{part.text}</mark
								>{:else}{part.text}{/if}{/each}</span
					>
				</div>
			{/each}
			<div style="height: {win.padBottom}px" aria-hidden="true"></div>
		{/if}
	</div>

	<footer class="status">
		<span class="num">
			{#if shown.length < feed.lines.length}{shown.length} of{/if}
			{feed.lines.length}
			{feed.lines.length === 1 ? 'line' : 'lines'}{feed.trimmed ? ' (the newest 5000)' : ''}
		</span>
		<span role="status">
			{#if !feed.following}Paused: turn on Follow to continue{:else if liveCount > 0}Live{:else if waiting}Connecting…{/if}
		</span>
		{#if feed.following && !atBottom && shown.length}
			<Button size="sm" variant="ghost" icon={ArrowDownToLine} onclick={toBottom}
				>Jump to latest</Button
			>
		{/if}
	</footer>
</section>

<style>
	.viewer {
		display: flex;
		flex-direction: column;
		min-height: 0;
		height: 100%;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
		overflow: hidden;
	}

	.toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-3);
		padding: var(--space-2) var(--space-3);
		border-bottom: 1px solid var(--border-subtle);
	}

	.search {
		display: flex;
		align-items: center;
		gap: 2px;
		flex: 1;
		min-width: 220px;
		max-width: 440px;
		color: var(--text-muted);
	}

	.search > :global(.field) {
		flex: 1;
		margin-right: var(--space-1);
	}

	.count {
		margin-left: var(--space-1);
		font-size: var(--text-caption);
		white-space: nowrap;
	}

	.count:empty {
		display: none;
	}

	.count.invalid {
		color: var(--danger);
	}

	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-3);
	}

	.view {
		display: flex;
		gap: 2px;
		padding-left: var(--space-3);
		border-left: 1px solid var(--border-subtle);
	}

	.follow {
		display: flex;
		padding-left: var(--space-3);
		border-left: 1px solid var(--border-subtle);
	}

	.actions {
		display: flex;
		align-items: center;
		gap: 2px;
		margin-left: auto;
	}

	.notices {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3) 0;
	}

	.body {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: var(--space-2) 0;
		background: var(--code-bg);
		font-size: 12.5px;
		outline: none;
	}

	.body:focus-visible {
		box-shadow: inset 0 0 0 2px var(--accent-text);
	}

	.line {
		display: flex;
		gap: var(--space-3);
		padding: 0 var(--space-3);
		line-height: 20px;
		white-space: pre;
		color: var(--text-default);
		width: max-content;
		min-width: 100%;
	}

	.line.wrap {
		width: auto;
		min-height: 20px;
		content-visibility: auto;
		contain-intrinsic-size: auto 20px;
	}

	.line.wrap .text {
		flex: 1;
		min-width: 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.line:hover {
		background: var(--code-active-line);
	}

	/* The edge marks the level; standard error without one gets a quiet edge. */
	.line.stderr {
		box-shadow: inset 2px 0 0 var(--border-strong);
	}

	.line[data-level='error'] {
		box-shadow: inset 2px 0 0 var(--danger);
	}

	.line[data-level='error'] .text {
		color: var(--code-url);
	}

	.line[data-level='warning'] {
		box-shadow: inset 2px 0 0 var(--warn);
	}

	.line[data-level='warning'] .text {
		color: var(--code-string);
	}

	.line[data-level='debug'] .text,
	.line[data-level='verbose'] .text {
		color: var(--text-muted);
	}

	.ts {
		flex: none;
		color: var(--text-muted);
	}

	.src {
		flex: none;
		min-width: 9ch;
	}

	.bar {
		flex: none;
		color: var(--text-faint);
	}

	mark {
		border-radius: 2px;
		background: var(--warn-soft);
		color: var(--warn);
		box-shadow: 0 0 0 1px var(--warn-border);
	}

	.empty {
		padding: var(--space-6) var(--space-4);
		font-family: var(--font-sans);
	}

	.status {
		display: flex;
		align-items: center;
		gap: var(--space-4);
		min-height: 30px;
		padding: 0 var(--space-3);
		border-top: 1px solid var(--border-subtle);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.dense .toolbar {
		padding: var(--space-1) var(--space-3);
	}

	@media (max-width: 767px) {
		.search {
			max-width: none;
			flex-basis: 100%;
		}

		.view,
		.follow {
			padding-left: 0;
			border-left: 0;
		}
	}
</style>
