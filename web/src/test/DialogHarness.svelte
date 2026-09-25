<script lang="ts">
	// Test harness: a trigger button and a Dialog with two focusable fields.
	import Button from '$lib/ui/Button.svelte';
	import Dialog from '$lib/ui/Dialog.svelte';

	let { dismissible = true }: { dismissible?: boolean } = $props();
	let open = $state(false);
</script>

<Button onclick={() => (open = true)}>Edit details</Button>
<button type="button">Elsewhere</button>
<Dialog bind:open title="Edit details" description="Display metadata" {dismissible}>
	<input aria-label="Description" />
	{#snippet footer()}
		<Button onclick={() => (open = false)}>Save details</Button>
	{/snippet}
</Dialog>
<p data-testid="state">{open ? 'open' : 'closed'}</p>
