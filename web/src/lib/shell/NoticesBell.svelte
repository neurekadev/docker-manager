<script lang="ts">
	// Notices bell (#22, #25 Q6): unread count and the list of in-app
	// notices (jobs finished or failed, environments offline, updates).
	// Opening the list marks them read when it closes.
	import Bell from '@lucide/svelte/icons/bell';
	import IconButton from '$lib/ui/IconButton.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import { formatRelative } from '$lib/ui/format';
	import { notices as defaultNotices, type Notices } from './notices.svelte';

	let { notices = defaultNotices }: { notices?: Notices } = $props();
	let open = $state(false);

	$effect(() => {
		if (!open) return;
		return () => notices.markAllRead();
	});

	const label = $derived(notices.unread ? `Notices, ${notices.unread} unread` : 'Notices');
</script>

<Popover bind:open label="Notices" width="380px">
	{#snippet trigger(props)}
		<IconButton {...props} {label} icon={Bell} badge={notices.unread} tooltipSide="bottom" />
	{/snippet}
	<div class="head">
		<h2>Notices</h2>
		{#if notices.items.length}
			<button type="button" class="link" onclick={() => notices.clear()}>Clear all</button>
		{/if}
	</div>
	{#if notices.items.length}
		<ul role="list" class="list">
			{#each notices.items as n (n.key)}
				<li class:unread={!n.read}>
					<span class="dot {n.tone}" aria-hidden="true"></span>
					<div class="text">
						{#if n.href}
							<a href={n.href} class="title" onclick={() => (open = false)}
								>{n.title}</a
							>
						{:else}
							<span class="title">{n.title}</span>
						{/if}
						{#if n.body}<p class="body">{n.body}</p>{/if}
						<span class="when">{formatRelative(new Date(n.at))}</span>
					</div>
				</li>
			{/each}
		</ul>
	{:else}
		<p class="empty">No notices. Finished jobs and offline environments show up here.</p>
	{/if}
</Popover>

<style>
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: var(--space-3) var(--space-4);
		border-bottom: 1px solid var(--border-subtle);
	}

	h2 {
		font-size: var(--text-control);
		line-height: var(--leading-control);
	}

	.link {
		padding: 0;
		border: 0;
		background: none;
		color: var(--accent-text);
		font-size: var(--text-caption);
	}

	.list {
		margin: 0;
		padding: var(--space-1);
	}

	li {
		display: flex;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-sm);
	}

	li.unread {
		background: color-mix(in srgb, var(--surface-selected) 60%, transparent);
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
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.body {
		color: var(--text-muted);
	}

	.when {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.empty {
		padding: var(--space-5) var(--space-4);
		color: var(--text-muted);
	}
</style>
