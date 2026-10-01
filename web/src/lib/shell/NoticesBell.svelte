<script lang="ts">
	// Notices bell (#22, #25 Q6, #159): the count of items not dismissed and
	// their list: the alerts that fire (disks and RAID, temperatures, disk
	// space and memory, offline environments, failed scheduled jobs,
	// available updates) and this tab's
	// notices of the user's own jobs. The badge stays until every item is
	// dismissed (closing the list changes nothing). Each item links to where
	// to act (the whole row is the link's target, its title the link's
	// name), says how bad it is and since when, and has a Dismiss button:
	// an alert the user may dismiss is dismissed for everyone, anything else
	// for this browser (notices.svelte.ts). "Dismiss all" does both at once;
	// "View all alerts" opens the Notifications page's Alerts tab. Focus stays in the list when
	// an item leaves it.
	import { tick } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import Bell from '@lucide/svelte/icons/bell';
	import X from '@lucide/svelte/icons/x';
	import { dismissMany, dismissOne } from '$lib/features/alerts/actions';
	import { severityLabel } from '$lib/features/alerts/model';
	import Button from '$lib/ui/Button.svelte';
	import IconButton from '$lib/ui/IconButton.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import { formatDateTime, formatRelative } from '$lib/ui/format';
	import {
		notices as defaultNotices,
		noticeHref,
		type BellItem,
		type Notices
	} from './notices.svelte';

	let {
		notices = defaultNotices,
		alertsHref
	}: {
		notices?: Notices;
		/** The Notifications page's Alerts tab, when the user may open it ("View all alerts"). */
		alertsHref?: string;
	} = $props();

	const qc = useQueryClient();
	let open = $state(false);
	let busy = $state(false);
	let heading = $state<HTMLHeadingElement | null>(null);
	let list = $state<HTMLUListElement | null>(null);

	const items = $derived(notices.list);
	const count = $derived(notices.count);
	const label = $derived(
		count ? `Notices, ${count} ${count === 1 ? 'item' : 'items'}` : 'Notices'
	);

	/** Keeps focus in the list: the item now at `index`, else the last one, else the heading. */
	async function refocus(index: number) {
		await tick();
		const buttons = list?.querySelectorAll<HTMLButtonElement>('.dismiss button') ?? [];
		const next = buttons[Math.min(index, buttons.length - 1)];
		if (next) next.focus();
		else heading?.focus();
	}

	async function dismiss(n: BellItem, index: number) {
		if (n.alert && n.serverDismiss) {
			// Hidden at once; the next list brings it back if it failed.
			notices.forgetAlerts([n.alert.id]);
			void refocus(index);
			await dismissOne(n.alert, { queryClient: qc, quiet: true });
			return;
		}
		notices.dismiss(n.dismissKey);
		void refocus(index);
	}

	async function dismissAll() {
		const server = items.filter((n) => n.alert && n.serverDismiss).map((n) => n.alert!.id);
		notices.dismiss(
			...items.filter((n) => !(n.alert && n.serverDismiss)).map((n) => n.dismissKey)
		);
		if (server.length) notices.forgetAlerts(server);
		void refocus(0);
		if (!server.length) return;
		busy = true;
		try {
			await dismissMany(server, { queryClient: qc, quiet: true, report: true });
		} finally {
			busy = false;
		}
	}
</script>

<Popover bind:open label="Notices" width="400px">
	{#snippet trigger(props)}
		<IconButton {...props} {label} icon={Bell} badge={count} tooltipSide="bottom" />
	{/snippet}
	<div class="head">
		<h2 bind:this={heading} tabindex="-1">Notices</h2>
	</div>
	{#if items.length}
		<ul role="list" class="list" bind:this={list}>
			{#each items as n, i (n.key)}
				<li>
					<span class="dot {n.tone}" aria-hidden="true"></span>
					<div class="text">
						<a href={noticeHref(n)} class="title" onclick={() => (open = false)}
							>{n.title}</a
						>
						{#if n.body}<p class="body">{n.body}</p>{/if}
						<span class="meta">
							{#if n.alert}<span class="severity {n.tone}"
									>{severityLabel(n.alert.severity)}</span
								><span class="sep" aria-hidden="true">·</span>{/if}
							{#if Number.isFinite(n.at)}<time
									datetime={new Date(n.at).toISOString()}
									title={formatDateTime(new Date(n.at))}
									>{formatRelative(new Date(n.at))}</time
								>{/if}
						</span>
					</div>
					<span class="dismiss">
						<IconButton
							size="sm"
							icon={X}
							label="Dismiss {n.title}"
							tooltipSide="left"
							onclick={() => dismiss(n, i)}
						/>
					</span>
				</li>
			{/each}
		</ul>
	{:else}
		<p class="empty">Nothing needs your attention.</p>
	{/if}
	{#if items.length || alertsHref}
		<div class="foot">
			{#if items.length}
				<Button size="sm" variant="ghost" loading={busy} onclick={dismissAll}
					>Dismiss all</Button
				>
			{/if}
			{#if alertsHref}
				<Button
					size="sm"
					variant="secondary"
					href={alertsHref}
					onclick={() => (open = false)}>View all alerts</Button
				>
			{/if}
		</div>
	{/if}
</Popover>

<style>
	.head {
		display: flex;
		align-items: center;
		min-height: 48px;
		padding: var(--space-2) var(--space-4);
		border-bottom: 1px solid var(--border-subtle);
	}

	h2 {
		font-size: var(--text-control);
		line-height: var(--leading-control);
		outline: none;
	}

	.list {
		margin: 0;
		padding: var(--space-1);
	}

	/* The row is the title link's target (its ::after covers the row); the
	   Dismiss button stays above it. */
	li {
		position: relative;
		display: flex;
		align-items: flex-start;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-2) var(--space-2) var(--space-3);
		border-radius: var(--radius-sm);
		transition: background-color var(--duration-fast) var(--ease-out);
	}

	li:hover {
		background: var(--surface-hover);
	}

	li:has(.title:focus-visible) {
		outline: var(--focus-ring);
		outline-offset: -2px;
	}

	.dot {
		flex-shrink: 0;
		width: 8px;
		height: 8px;
		margin-top: 6px;
		border-radius: var(--radius-full);
		background: var(--info);
	}

	.dot.ok {
		background: var(--ok);
	}
	.dot.warn {
		background: var(--warn);
	}
	.dot.danger {
		background: var(--danger);
	}

	.text {
		flex: 1 1 auto;
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		text-decoration: none;
		overflow-wrap: anywhere;
	}

	.title::after {
		content: '';
		position: absolute;
		inset: 0;
		border-radius: inherit;
	}

	.title:focus-visible {
		outline: none;
	}

	li:hover .title {
		color: var(--accent-text);
	}

	.body {
		color: var(--text-muted);
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1);
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.severity {
		font-weight: var(--weight-medium);
	}

	.severity.danger {
		color: var(--danger);
	}

	.severity.warn {
		color: var(--warn);
	}

	.severity.info {
		color: var(--info);
	}

	.dismiss {
		position: relative;
		z-index: 1;
		flex-shrink: 0;
		margin-top: -3px;
	}

	.empty {
		padding: var(--space-5) var(--space-4);
		color: var(--text-muted);
	}

	/* Stays in view while the list scrolls. */
	.foot {
		position: sticky;
		bottom: 0;
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border-top: 1px solid var(--border-subtle);
		background: var(--surface-raised);
	}
</style>
