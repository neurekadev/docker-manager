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
	This is the last known state. DockYard reconnects on its own when the agent is back; actions that
	need
	{name} wait until then.
</Notice>
