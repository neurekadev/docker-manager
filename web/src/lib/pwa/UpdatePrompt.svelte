<script lang="ts">
	// "New version available" prompt (#11, #23). The update is applied only
	// when the user clicks Reload; nothing reloads automatically. Unstyled
	// until the design system (#22).
	// Critical work ($lib/live criticalWork: unsaved edits, terminals,
	// restores, uploads) holds the reload back until it is finished.
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { criticalWork } from '$lib/live/critical.svelte';
	import { pwa } from './register.svelte';
</script>

<div role="status" aria-live="polite" aria-label="App update" data-testid="update-prompt">
	{#if pwa.updateAvailable}
		<span>A new version of DockYard is available.</span>
		{#if criticalWork.active}
			<span data-testid="update-blocked">
				Finish first: {criticalWork.items.map((i) => i.label).join(', ')}
			</span>
		{/if}
		<button
			type="button"
			onclick={() => pwa.applyUpdate()}
			disabled={pwa.updating || criticalWork.active}
		>
			<RefreshCw aria-hidden="true" />
			Reload to update
		</button>
		<button type="button" onclick={() => pwa.dismiss()} disabled={pwa.updating}>Later</button>
	{/if}
</div>
