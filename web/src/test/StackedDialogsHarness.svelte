<script lang="ts">
	// Test harness: "Confirm It's You" mounts before "Invite a User" (like the
	// app shell's step-up check and a page dialog) but opens after it.
	import Button from '$lib/ui/Button.svelte';
	import Dialog from '$lib/ui/Dialog.svelte';

	let stepUp = $state(false);
	let invite = $state(false);
</script>

<Dialog bind:open={stepUp} title="Confirm It's You">
	<input aria-label="Password" />
</Dialog>
<Button onclick={() => (invite = true)}>Invite User</Button>
<Dialog bind:open={invite} title="Invite a User">
	<Button onclick={() => (stepUp = true)}>Create Invitation</Button>
</Dialog>
