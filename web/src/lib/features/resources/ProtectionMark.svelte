<script lang="ts">
	// The compact "Docker Manager" mark of list rows (#32, #22 polish): a
	// small shield next to the name, the reason as its tooltip and, for
	// screen readers, as text. It keeps rows one line high; detail pages
	// use ProtectionBadge.
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { plain, protectionLabel, type Protection } from './refusals';

	let { protection }: { protection: Protection } = $props();
</script>

<span class="mark" title="{protectionLabel(protection)}: {plain(protection.reason)}">
	<ShieldCheck size={14} strokeWidth={1.75} aria-hidden="true" />
	<span class="sr-only">Docker Manager: {plain(protection.reason)}</span>
</span>

<style>
	.mark {
		position: relative;
		display: inline-flex;
		flex: none;
		align-items: center;
		color: var(--accent-text);
	}
</style>
