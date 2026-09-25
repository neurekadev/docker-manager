<script lang="ts">
	// Copy to clipboard (#22): request IDs, paths, digests, one-time secrets.
	// Confirms with "Copied" (announced) for two seconds.
	import Check from '@lucide/svelte/icons/check';
	import Copy from '@lucide/svelte/icons/copy';
	import IconButton from './IconButton.svelte';
	import Button from './Button.svelte';

	interface Props {
		value: string;
		/** What is copied, e.g. "request ID" → "Copy request ID". */
		what: string;
		/** Show a text button instead of an icon button. */
		text?: boolean;
		size?: 'sm' | 'md';
		oncopied?: () => void;
	}

	let { value, what, text = false, size = 'sm', oncopied }: Props = $props();
	let copied = $state(false);
	let failed = $state(false);

	async function copy() {
		try {
			await navigator.clipboard.writeText(value);
			copied = true;
			failed = false;
			oncopied?.();
			setTimeout(() => (copied = false), 2000);
		} catch {
			failed = true;
		}
	}
</script>

{#if text}
	<Button {size} icon={copied ? Check : Copy} onclick={copy}
		>{copied ? 'Copied' : `Copy ${what}`}</Button
	>
{:else}
	<IconButton
		{size}
		label={copied ? 'Copied' : `Copy ${what}`}
		icon={copied ? Check : Copy}
		onclick={copy}
	/>
{/if}
<span class="sr-only" role="status"
	>{copied
		? `Copied ${what}`
		: failed
			? `Could not copy the ${what}; select and copy it by hand`
			: ''}</span
>
