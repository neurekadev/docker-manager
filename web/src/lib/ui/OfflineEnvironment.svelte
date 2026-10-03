<script lang="ts">
	// Offline environment banner (#3, #22): the environment's agent is not
	// connected, so the page shows its last known state. Never colour alone:
	// the text says "offline" and since when.
	import WifiOff from '@lucide/svelte/icons/wifi-off';
	import Notice from './Notice.svelte';
	import { formatRelative } from './format';

	interface Props {
		name: string;
		/** When it went offline (Environment.connectionChangedAt). */
		since?: string;
		now?: Date;
	}

	let { name, since, now }: Props = $props();
</script>

<Notice tone="offline" icon={WifiOff} title="{name} is offline">
	{#if since}Its agent disconnected {formatRelative(since, now)}.{/if}
	Showing the last known state. Actions on {name} are unavailable until it reconnects.
</Notice>
