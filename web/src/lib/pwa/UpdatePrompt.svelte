<script lang="ts">
	// "New version available" prompt (#11, #23), styled by the design system
	// (#22). The update is applied only when the user clicks Reload; nothing
	// reloads automatically. Critical work ($lib/live criticalWork: unsaved
	// edits, terminals, restores, uploads) holds the reload back until it is
	// finished.
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Button from '$lib/ui/Button.svelte';
	import { criticalWork } from '$lib/live/critical.svelte';
	import { pwa } from './register.svelte';
</script>

<div role="status" aria-live="polite" aria-label="App Update" data-testid="update-prompt">
	{#if pwa.updateAvailable}
		<div class="card">
			<p>A new version of Docker Manager is available.</p>
			{#if criticalWork.active}
				<p class="blocked" data-testid="update-blocked">
					Finish first: {criticalWork.items.map((i) => i.label).join(', ')}
				</p>
			{/if}
			<div class="actions">
				<Button
					variant="ghost"
					size="sm"
					onclick={() => pwa.dismiss()}
					disabled={pwa.updating}>Later</Button
				>
				<Button
					variant="primary"
					size="sm"
					icon={RefreshCw}
					onclick={() => pwa.applyUpdate()}
					disabled={criticalWork.active}
					loading={pwa.updating}>Reload to Update</Button
				>
			</div>
		</div>
	{/if}
</div>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		max-width: 420px;
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
		color: var(--text-strong);
		pointer-events: auto;
	}

	.blocked {
		color: var(--warn);
		font-size: var(--text-body);
	}

	.actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-2);
	}
</style>
