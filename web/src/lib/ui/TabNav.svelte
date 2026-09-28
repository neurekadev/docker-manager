<script lang="ts" module>
	export interface TabLink {
		href: string;
		label: string;
		/** A chip after the tab (e.g. "Undeployed changes" beside the tabs). */
		count?: number;
	}
</script>

<script lang="ts">
	// Route tabs (#22 stack detail: Overview · Files · Logs · Terminal ·
	// Revisions · Policies · Activity). Links with aria-current="page"; the
	// URL is the state, so reloads and deep links keep the tab.
	import type { Snippet } from 'svelte';

	interface Props {
		items: TabLink[];
		/** The current path (page.url.pathname). */
		current: string;
		label: string;
		/** Content after the tabs (e.g. an "Undeployed changes" chip). */
		after?: Snippet;
	}

	let { items, current, label, after }: Props = $props();

	function active(href: string): boolean {
		// The first tab is the base path; others match by prefix.
		return href === items[0]?.href ? current === href : current.startsWith(href);
	}

	// On narrow screens the tabs scroll sideways: keep the current one in
	// view (deep links to a later tab would otherwise hide it).
	let nav = $state<HTMLElement>();
	$effect(() => {
		void current;
		const el = nav?.querySelector<HTMLElement>('[aria-current="page"]');
		if (!nav || !el) return;
		const left = el.offsetLeft - nav.offsetLeft;
		if (left < nav.scrollLeft || left + el.offsetWidth > nav.scrollLeft + nav.clientWidth)
			nav.scrollLeft = Math.max(0, left - 16);
		measure();
	});

	// Tabs cut off at an edge fade out there, so a sideways-scrolling row
	// shows that more tabs follow.
	let moreBefore = $state(false);
	let moreAfter = $state(false);
	function measure() {
		if (!nav) return;
		moreBefore = nav.scrollLeft > 1;
		moreAfter = nav.scrollLeft + nav.clientWidth < nav.scrollWidth - 1;
	}
	$effect(() => {
		if (!nav || typeof ResizeObserver === 'undefined') return;
		const observer = new ResizeObserver(measure);
		observer.observe(nav);
		return () => observer.disconnect();
	});
</script>

<div class="tabnav">
	<nav
		aria-label={label}
		bind:this={nav}
		onscroll={measure}
		class:fade-start={moreBefore}
		class:fade-end={moreAfter}
	>
		<ul role="list">
			{#each items as item (item.href)}
				<li>
					<a
						href={item.href}
						class="tab"
						aria-current={active(item.href) ? 'page' : undefined}
					>
						{item.label}
						{#if item.count !== undefined}<span class="count num">{item.count}</span
							>{/if}
					</a>
				</li>
			{/each}
		</ul>
	</nav>
	{#if after}<div class="after">{@render after()}</div>{/if}
</div>

<style>
	.tabnav {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		border-bottom: 1px solid var(--border-subtle);
	}

	/* Sideways only: a vertical swipe on the tabs scrolls the page instead of
	   bouncing the row (overflow-x: auto alone makes overflow-y auto too).
	   The 1 px bottom padding holds the current tab's underline, which
	   covers the row's border (the negative margin keeps the layout). */
	nav {
		min-width: 0;
		overflow-x: auto;
		overflow-y: hidden;
		overscroll-behavior-x: contain;
		scrollbar-width: none;
		padding-bottom: 1px;
		margin-bottom: -1px;
		--fade: 40px;
	}

	nav.fade-end {
		mask-image: linear-gradient(to right, black calc(100% - var(--fade)), transparent);
	}

	nav.fade-start {
		mask-image: linear-gradient(to left, black calc(100% - var(--fade)), transparent);
	}

	nav.fade-start.fade-end {
		mask-image: linear-gradient(
			to right,
			transparent,
			black var(--fade),
			black calc(100% - var(--fade)),
			transparent
		);
	}

	ul {
		display: flex;
		gap: var(--space-1);
		margin: 0;
	}

	.tab {
		position: relative;
		display: inline-flex;
		align-items: center;
		gap: 6px;
		height: 40px;
		padding: 0 var(--space-3);
		border-radius: var(--radius-sm) var(--radius-sm) 0 0;
		color: var(--text-muted);
		font-size: var(--text-control);
		font-weight: var(--weight-medium);
		white-space: nowrap;
	}

	.tab:hover {
		color: var(--text-strong);
		text-decoration: none;
	}

	.tab[aria-current='page'] {
		color: var(--text-strong);
		background: var(--surface-selected);
	}

	.tab[aria-current='page']::after {
		content: '';
		position: absolute;
		left: 0;
		right: 0;
		bottom: -1px;
		height: 2px;
		background: var(--accent);
	}

	.count {
		min-width: 18px;
		padding: 0 5px;
		border-radius: var(--radius-full);
		background: var(--surface-raised);
		font-size: 11px;
		line-height: 18px;
		text-align: center;
	}

	.after {
		flex-shrink: 0;
	}

	@media (max-width: 767px) {
		/* The chip gets its own line instead of pushing tabs out of view. */
		.tabnav {
			flex-wrap: wrap;
			row-gap: var(--space-2);
		}

		nav {
			flex: 1 1 100%;
		}

		.after {
			order: -1;
		}
	}
</style>
