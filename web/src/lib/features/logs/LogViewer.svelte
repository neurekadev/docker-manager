<script lang="ts">
	// Log viewer (#8, #22 the mockup's log panel): container or stack
	// service logs merged by time, each service in its hue (the service hue
	// identity), service filter chips, Follow (live stream with cursor
	// resume), timestamps, line wrap, search with highlighting ("Matching
	// lines only"), "Errors only" (standard error), clear view, download
	// and pop-out. Unwrapped lines render in a fixed-height window, so
	// thousands of lines stay fast (wrapped lines render all, skipping the
	// off-screen ones' layout). Log lines are never stored beyond the page.
	import { onDestroy } from 'svelte';
	import ArrowDownToLine from '@lucide/svelte/icons/arrow-down-to-line';
	import Download from '@lucide/svelte/icons/download';
	import Eraser from '@lucide/svelte/icons/eraser';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import type { Snippet } from 'svelte';
	import { serviceSeriesColor, TILE_HEX } from '$lib/design/hue';
	import {
		Button,
		Chip,
		EmptyState,
		IconButton,
		Notice,
		Skeleton,
		Switch,
		TextField,
		toast
	} from '$lib/ui';
	import { virtualWindow } from '$lib/ui/table';
	import { endReason, type LogFeed, type LogLine } from './feed.svelte';
	import {
		countMatches,
		filterLines,
		formatLogTime,
		highlight,
		logText,
		serviceShown,
		toggleHidden
	} from './format';

	interface Props {
		feed: LogFeed;
		/** Accessible name of the log region, e.g. "Logs of Silo". */
		label: string;
		/** Stack logs: service hues and filter chips. */
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
	let errorsOnly = $state(false);
	let matchingOnly = $state(false);
	let query = $state('');
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
	const chipState = $derived({ only, hidden, services: names });
	const isShown = (svc: string) => serviceShown(svc, chipState);
	const allShown = $derived(names.every(isShown));
	// Service chips and "Errors only" first; the search count reads these.
	const visible = $derived(
		filterLines(feed.lines, {
			source: allShown ? undefined : (key) => isShown(serviceOf[key] ?? ''),
			errorsOnly
		})
	);
	const q = $derived(query.trim().toLowerCase());
	const matches = $derived(countMatches(visible, q));
	const shown = $derived(matchingOnly && q ? filterLines(visible, { query: q }) : visible);

	/** The service's colour: its tile colour, else the stable hue (#22). */
	function hueOf(svc: string): string {
		const color = feed.sources.find((s) => (s.service ?? s.label) === svc)?.color;
		if (color) return TILE_HEX[color].fg;
		return stackId ? serviceSeriesColor(stackId, svc) : TILE_HEX.blue.fg;
	}

	function toggleService(svc: string) {
		hidden = toggleHidden(svc, chipState);
		only = null;
	}

	function showAll() {
		hidden = [];
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
			<div class="chips" role="group" aria-label="Services">
				<Chip size="sm" label="All services" selected={allShown} onclick={showAll} />
				{#each services as s (s.service)}
					<Chip
						size="sm"
						label={s.service}
						hue={hueOf(s.service)}
						selected={isShown(s.service)}
						onclick={() => toggleService(s.service)}
					/>
				{/each}
			</div>
		{/if}
		<div class="search" role="search" aria-label="Search logs">
			<TextField
				label="Search logs"
				hideLabel
				type="search"
				placeholder="Search logs"
				bind:value={query}
			/>
			{#if q}<span class="count num" aria-live="polite"
					>{matches} {matches === 1 ? 'match' : 'matches'}</span
				>{/if}
		</div>
		<div class="switches">
			<Switch bind:checked={matchingOnly} label="Matching lines only" />
			<Switch bind:checked={errorsOnly} label="Errors only" />
			<Switch bind:checked={wrap} label="Wrap lines" />
			<Switch bind:checked={timestamps} label="Timestamps" />
			<Switch
				checked={feed.following}
				label="Follow"
				onchange={(on) => feed.setFollowing(on)}
			/>
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
				{#if waiting}
					<div aria-busy="true"><Skeleton lines={5} /></div>
				{:else if feed.lines.length > 0 && !allShown && !names.some(isShown)}
					<EmptyState
						compact
						icon={ScrollText}
						title="Every service is hidden"
						description="Choose a service above to show its lines."
					/>
				{:else if visible.length > 0}
					<EmptyState
						compact
						icon={ScrollText}
						title="No lines match “{query.trim()}”"
						description="Change the search, or turn off Matching lines only to see every line."
					/>
				{:else if feed.lines.length > 0 && errorsOnly}
					<EmptyState
						compact
						icon={ScrollText}
						title="No error lines"
						description="None of these lines were written as errors. Turn off Errors only to see every line."
					/>
				{:else if feed.lines.length > 0}
					<EmptyState
						compact
						icon={ScrollText}
						title="No lines from the selected services"
						description="Choose another service above, or All services."
					/>
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
						>{#each highlight(l.text, q) as part, i (i)}{#if part.match}<mark
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
			{shown.length}
			{shown.length === 1 ? 'line' : 'lines'}{feed.trimmed ? ' (the newest 5000)' : ''}
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

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1);
	}

	.search {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		flex: 1;
		min-width: 180px;
		max-width: 320px;
		color: var(--text-muted);
	}

	.search > :global(.field) {
		flex: 1;
	}

	.count {
		font-size: var(--text-caption);
		white-space: nowrap;
	}

	.switches {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2) var(--space-4);
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

	.line.stderr {
		box-shadow: inset 2px 0 0 var(--danger);
	}

	.line.stderr .text {
		color: var(--code-url);
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
	}
</style>
