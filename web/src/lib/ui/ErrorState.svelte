<script lang="ts">
	// Error state (#22) for a failed load, built from the API error shape:
	// what failed (the optional title) and what happened (the message) come
	// first; the stable code and the request ID (with a copy button, for the
	// manager logs) sit behind "Details"; Retry shows when repeating may
	// help. `bare` drops the frame for use inside a Card.
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import ServerOff from '@lucide/svelte/icons/server-off';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import Button from './Button.svelte';
	import CopyButton from './CopyButton.svelte';
	import IconTile from './IconTile.svelte';
	import { errorView } from './errors';

	interface Props {
		error: unknown;
		/** What failed, e.g. "The stacks could not be loaded." Without it the message leads. */
		title?: string;
		onretry?: () => void;
		retrying?: boolean;
		compact?: boolean;
		/** No border, background or margin (inside a Card). */
		bare?: boolean;
	}

	let {
		error,
		title,
		onretry,
		retrying = false,
		compact = false,
		bare = false
	}: Props = $props();
	const v = $derived(errorView(error));
	const uid = $props.id();
	let details = $state(false);
</script>

<div class="error-state" class:compact class:bare role="alert">
	<IconTile
		icon={v.network ? ServerOff : TriangleAlert}
		color="rose"
		size={compact || bare ? 'md' : 'lg'}
	/>
	<div class="text">
		{#if title}
			<p class="title">{title}</p>
			<p class="message">{v.message}</p>
		{:else}
			<p class="title">{v.message}</p>
		{/if}
		{#if v.code || v.requestId}
			<button
				type="button"
				class="toggle"
				aria-expanded={details}
				aria-controls="{uid}-details"
				onclick={() => (details = !details)}
			>
				<ChevronRight size={14} strokeWidth={1.75} aria-hidden="true" class="chev" />
				Details
			</button>
			{#if details}
				<dl class="meta" id="{uid}-details">
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
		{/if}
	</div>
	{#if onretry && (v.retryable || v.network || v.status === null)}
		<div class="retry">
			<Button icon={RotateCw} loading={retrying} onclick={onretry}>Retry</Button>
		</div>
	{/if}
</div>

<style>
	.error-state {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-start;
		gap: var(--space-3) var(--space-4);
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

	.bare {
		max-width: none;
		margin: 0;
		padding: 0;
		border: 0;
		border-radius: 0;
		background: none;
	}

	/* Wide enough for a sentence; Retry wraps below it on phones. */
	.text {
		flex: 1 1 240px;
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-size: var(--text-section);
		line-height: var(--leading-section);
		font-weight: var(--weight-semibold);
	}

	.bare .title,
	.compact .title {
		font-size: var(--text-subsection);
		line-height: var(--leading-subsection);
	}

	.message {
		margin-top: 2px;
		color: var(--text-default);
		font-size: var(--text-control);
	}

	.toggle {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		margin-top: var(--space-2);
		padding: 0;
		border: 0;
		border-radius: var(--radius-sm);
		background: none;
		color: var(--accent-text);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.toggle :global(.chev) {
		transition: transform var(--duration-fast) var(--ease-out);
	}

	.toggle[aria-expanded='true'] :global(.chev) {
		transform: rotate(90deg);
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-6);
		margin-top: var(--space-2);
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
		overflow-wrap: anywhere;
	}

	.retry {
		flex-shrink: 0;
	}
</style>
