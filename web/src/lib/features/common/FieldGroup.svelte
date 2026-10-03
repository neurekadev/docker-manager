<script lang="ts">
	// A group of related controls (days of a window, retention rules, a
	// rule's filters): a fieldset with a visible legend, an optional (i)
	// after it (`info`, for an explanation needed only now and then) and a
	// visible hint (`hint`, for what the user must know, such as a data
	// consistency warning). Both describe the group; neither joins its name.
	import type { Snippet } from 'svelte';
	import { InfoTip } from '$lib/ui';

	let {
		legend,
		hint,
		info,
		children
	}: { legend: string; hint?: string; info?: string; children: Snippet } = $props();
	const uid = $props.id();
</script>

<fieldset
	class="group"
	aria-labelledby="fg-{uid}-legend"
	aria-describedby={[hint ? `fg-${uid}-hint` : '', info ? `fg-${uid}-info` : '']
		.filter(Boolean)
		.join(' ') || undefined}
>
	<legend
		><span id="fg-{uid}-legend">{legend}</span>{#if info}<InfoTip
				id="fg-{uid}-info"
				text={info}
			/>{/if}</legend
	>
	{#if hint}<p class="hint" id="fg-{uid}-hint">{hint}</p>{/if}
	<div class="body">{@render children()}</div>
</fieldset>

<style>
	.group {
		margin: 0;
		padding: var(--space-3) var(--space-4) var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		min-width: 0;
	}

	legend {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		padding: 0 var(--space-1);
		color: var(--text-default);
		font-weight: var(--weight-medium);
	}

	.hint {
		margin-bottom: var(--space-3);
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.body {
		display: grid;
		gap: var(--space-3);
		min-width: 0;
	}
</style>
