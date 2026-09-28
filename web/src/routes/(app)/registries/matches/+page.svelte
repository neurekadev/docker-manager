<script lang="ts">
	// Which connection does an image use? (#19 match preview) is the "Test
	// an image" dialog of the registry connections now; this route keeps old
	// links working by opening it there.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { routes } from '$lib/routes';
	import { Skeleton } from '$lib/ui';

	onMount(() => {
		void goto(routes.registryMatches(), { replaceState: true });
	});
</script>

<div aria-busy="true"><Skeleton height="120px" radius="lg" /></div>
