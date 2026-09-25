<script lang="ts">
	// Inline notice / banner (#22): offline environment, external file change
	// ("silo/compose.yaml changed on disk. Your edits are kept."), undeployed
	// changes, app update. The text names the situation and the next step;
	// actions go in the actions snippet.
	import type { Snippet } from 'svelte';
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import Info from '@lucide/svelte/icons/info';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import WifiOff from '@lucide/svelte/icons/wifi-off';
	import type { IconComponent } from '$lib/design/icons';

	interface Props {
		tone?: 'info' | 'warn' | 'danger' | 'offline';
		title: string;
		icon?: IconComponent;
		/** status (polite, default) or alert (assertive) live region; none for static notices. */
		live?: 'status' | 'alert' | 'none';
		children?: Snippet;
		actions?: Snippet;
		/** Full-width bar under the top bar instead of a rounded card. */
		bar?: boolean;
	}

	let {
		tone = 'info',
		title,
		icon,
		live = 'status',
		children,
		actions,
		bar = false
	}: Props = $props();
	const defaults = { info: Info, warn: TriangleAlert, danger: CircleAlert, offline: WifiOff };
	const Icon = $derived(icon ?? defaults[tone]);
</script>

<div class="notice {tone}" class:bar role={live === 'none' ? undefined : live}>
	<Icon size={18} strokeWidth={1.75} aria-hidden="true" class="notice-icon" />
	<div class="text">
		<p class="title">{title}</p>
		{#if children}<div class="body notice-body">{@render children()}</div>{/if}
	</div>
	{#if actions}<div class="actions">{@render actions()}</div>{/if}
</div>

<style>
	.notice {
		display: flex;
		align-items: flex-start;
		gap: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	.bar {
		align-items: center;
		border-width: 0 0 1px;
		border-radius: 0;
		padding: var(--space-2) var(--page-gutter);
	}

	.notice :global(.notice-icon) {
		margin-top: 1px;
	}

	.bar :global(.notice-icon) {
		margin-top: 0;
	}

	.info {
		border-color: color-mix(in srgb, var(--info) 30%, transparent);
		background: var(--info-soft);
	}
	.info :global(.notice-icon) {
		color: var(--info);
	}
	.warn {
		border-color: var(--warn-border);
		background: var(--warn-soft);
	}
	.warn :global(.notice-icon) {
		color: var(--warn);
	}
	.danger {
		border-color: var(--danger-border);
		background: var(--danger-soft);
	}
	.danger :global(.notice-icon) {
		color: var(--danger);
	}
	.offline {
		background: var(--offline-soft);
	}
	.offline :global(.notice-icon) {
		color: var(--offline);
	}

	.text {
		flex: 1;
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.body {
		margin-top: 2px;
		color: var(--text-default);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
		align-self: center;
	}

	/* Phones: actions move under the text instead of squeezing it. */
	@media (max-width: 767px) {
		.notice:not(.bar) {
			flex-wrap: wrap;
		}

		.notice:not(.bar) .actions {
			flex-basis: 100%;
			padding-left: calc(18px + var(--space-3));
		}
	}
</style>
