<script lang="ts">
	import ArrowUpCircle from '@lucide/svelte/icons/arrow-up-circle';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import { Badge } from '$lib/ui';

	let { status }: { status?: string } = $props();
</script>

{#if status === 'update_available'}
	<Badge tone="warn"><ArrowUpCircle size={14} aria-hidden="true" /> Update available</Badge>
{:else if status === 'up_to_date'}
	<Badge tone="ok"><CircleCheck size={14} aria-hidden="true" /> Up to date</Badge>
{/if}
