<script lang="ts">
	// A SMART value's verdict in the disk details (#210): the app's OK,
	// warning or danger icon (as in toasts and notices) in its tone colour,
	// then a short label ("OK", "Failed in the past", "8 reallocated
	// sectors"). The label carries the meaning; the icon is decorative.
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import type { SmartCheck } from './diskHealth';

	let { check }: { check: SmartCheck } = $props();

	const ICONS = { ok: CircleCheck, warn: TriangleAlert, danger: CircleAlert };
	const Icon = $derived(ICONS[check.tone]);
</script>

<span class="check {check.tone}" data-tone={check.tone}
	><span class="icon"><Icon size={14} strokeWidth={2} aria-hidden="true" /></span><span
		>{check.label}</span
	></span
>

<style>
	.check {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
	}

	.icon {
		display: inline-flex;
		flex: none;
	}

	.ok .icon {
		color: var(--ok);
	}

	.warn {
		color: var(--warn);
		font-weight: var(--weight-medium);
	}

	.danger {
		color: var(--danger);
		font-weight: var(--weight-medium);
	}
</style>
