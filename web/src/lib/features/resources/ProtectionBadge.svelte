<script lang="ts">
	// "Docker Manager" (#32): marks Docker Manager's own containers, images,
	// volumes and networks. The reason is in the title and, for screen
	// readers, in the text; detail pages show it in full.
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { Badge } from '$lib/ui';
	import { plain, protectionLabel, type Protection } from './refusals';

	interface Props {
		protection: Protection;
	}

	let { protection }: Props = $props();
</script>

<Badge tone="accent" title="{protectionLabel(protection)}: {plain(protection.reason)}">
	<ShieldCheck size={13} strokeWidth={1.75} aria-hidden="true" />
	Docker Manager<span class="sr-only">: {plain(protection.reason)}</span>
</Badge>
