<script lang="ts">
	// Error state (#22) for a failed load, built from the API error shape:
	// the message (what happened), the stable code, the request ID (with a
	// copy button, for the manager logs) and Retry when repeating may help.
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import ServerOff from '@lucide/svelte/icons/server-off';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import Button from './Button.svelte';
	import CopyButton from './CopyButton.svelte';
	import IconTile from './IconTile.svelte';
	import { errorView } from './errors';

	interface Props {
		error: unknown;
		/** What failed, e.g. "The stacks could not be loaded." */
		title?: string;
		onretry?: () => void;
		retrying?: boolean;
		compact?: boolean;
	}

	let {
		error,
		title = 'This could not be loaded.',
		onretry,
		retrying = false,
		compact = false
	}: Props = $props();
	const v = $derived(errorView(error));
</script>

<div class="error-state" class:compact role="alert">
	<IconTile
		icon={v.network ? ServerOff : TriangleAlert}
		color="rose"
		size={compact ? 'md' : 'lg'}
	/>
	<div class="text">
		<p class="title">{title}</p>
		<p class="message">{v.message}</p>
		{#if v.code || v.requestId}
			<dl class="meta">
				{#if v.code}<div>
						<dt>Code</dt>
						<dd class="mono">{v.code}</dd>
					</div>{/if}
				{#if v.requestId}
					<div>
						<dt>Request ID</dt>
						<dd class="mono">
							{v.requestId}
							<CopyButton value={v.requestId} what="request ID" />
						</dd>
					</div>
				{/if}
			</dl>
		{/if}
	</div>
	{#if onretry && (v.retryable || v.network || v.status === null)}
		<Button icon={RotateCw} loading={retrying} onclick={onretry}>Retry</Button>
	{/if}
</div>

<style>
	.error-state {
		display: flex;
		align-items: flex-start;
		gap: var(--space-4);
		max-width: 640px;
		margin: var(--space-8) auto;
		padding: var(--space-5);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.compact {
		margin: 0;
		padding: var(--space-4);
	}

	.text {
		flex: 1;
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-size: var(--text-section);
		line-height: var(--leading-section);
		font-weight: var(--weight-semibold);
	}

	.message {
		margin-top: 2px;
		color: var(--text-default);
		font-size: var(--text-control);
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-6);
		margin-top: var(--space-3);
	}

	.meta div {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}

	dt {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	dd {
		display: flex;
		align-items: center;
		gap: var(--space-1);
		margin: 0;
		color: var(--text-default);
	}
</style>
