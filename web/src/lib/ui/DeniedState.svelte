<script lang="ts">
	// Denied / Restricted state (#17, #22): a signed-in user without access
	// sees a clear, calm explanation, never an error. The default copy is
	// the design brief's Restricted-user text.
	import LockKeyhole from '@lucide/svelte/icons/lock-keyhole';
	import EmptyState from './EmptyState.svelte';

	interface Props {
		title?: string;
		description?: string;
		level?: 1 | 2 | 3;
	}

	let {
		title = "You don't have access to anything yet.",
		description = 'Ask the owner of this Docker Manager to grant access.',
		level = 2
	}: Props = $props();
</script>

<div data-testid="denied-state">
	<EmptyState icon={LockKeyhole} color="slate" {title} {description} {level} />
</div>
