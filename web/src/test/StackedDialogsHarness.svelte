<script lang="ts">
	// Test harness: "Confirm it's you" mounts before "Invite a user" (like the
	// app shell's step-up check and a page dialog) but opens after it.
	import Button from '$lib/ui/Button.svelte';
	import Dialog from '$lib/ui/Dialog.svelte';

	let stepUp = $state(false);
	let invite = $state(false);
</script>

<Dialog bind:open={stepUp} title="Confirm it's you">
	<input aria-label="Password" />
</Dialog>
<Button onclick={() => (invite = true)}>Invite user</Button>
<Dialog bind:open={invite} title="Invite a user">
	<Button onclick={() => (stepUp = true)}>Create invitation</Button>
</Dialog>
