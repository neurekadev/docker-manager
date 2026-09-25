<script lang="ts" module>
	export type BadgeTone = 'neutral' | 'accent' | 'ok' | 'warn' | 'danger' | 'info' | 'offline';
</script>

<script lang="ts">
	// Badge (#22): a compact label. With `dot`, a status dot precedes the
	// text; the text always carries the meaning (never colour alone).
	import type { Snippet } from 'svelte';

	interface Props {
		tone?: BadgeTone;
		dot?: boolean;
		/** Pulse the dot (in-progress states); static under reduced motion. */
		pulse?: boolean;
		title?: string;
		children: Snippet;
	}

	let { tone = 'neutral', dot = false, pulse = false, title, children }: Props = $props();
</script>

<span class="badge {tone}" {title}>
	{#if dot}<span class="dot" class:pulse aria-hidden="true"></span>{/if}
	{@render children()}
</span>

<style>
	.badge {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		height: 24px;
		padding: 0 var(--space-2);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
		color: var(--text-default);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		font-weight: var(--weight-medium);
		white-space: nowrap;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
		background: currentColor;
	}

	.pulse {
		animation: breathe 1.6s ease-in-out infinite;
	}

	@keyframes breathe {
		50% {
			opacity: 0.35;
		}
	}

	.accent {
		background: var(--accent-soft);
		border-color: color-mix(in srgb, var(--accent) 40%, transparent);
		color: var(--accent-text);
	}
	.ok {
		background: var(--ok-soft);
		border-color: var(--ok-border);
		color: var(--ok);
	}
	.warn {
		background: var(--warn-soft);
		border-color: var(--warn-border);
		color: var(--warn);
	}
	.danger {
		background: var(--danger-soft);
		border-color: var(--danger-border);
		color: var(--danger);
	}
	.info {
		background: var(--info-soft);
		border-color: color-mix(in srgb, var(--info) 35%, transparent);
		color: var(--info);
	}
	.offline {
		background: var(--offline-soft);
		border-color: var(--border-strong);
		color: var(--offline);
	}

	@media (prefers-reduced-motion: reduce) {
		.pulse {
			animation: none;
		}
	}
</style>
