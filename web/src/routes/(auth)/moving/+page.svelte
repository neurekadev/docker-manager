<script lang="ts">
	// The new server's move status (public; the root layout sends every page
	// here while this Docker Manager waits for a move: WaitingStatus).
	import WaitingStatus from '$lib/features/managermove/WaitingStatus.svelte';
</script>

<WaitingStatus />
