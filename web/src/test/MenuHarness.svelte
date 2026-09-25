<script lang="ts">
	// Test harness: an IconButton-triggered Menu and a SplitButton.
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import IconButton from '$lib/ui/IconButton.svelte';
	import Menu from '$lib/ui/Menu.svelte';
	import SplitButton from '$lib/ui/SplitButton.svelte';

	let { onpick }: { onpick: (what: string) => void } = $props();
</script>

<Menu
	label="Actions for silo-web"
	items={[
		{ label: 'Restart', onSelect: () => onpick('restart') },
		{ label: 'Disabled action', disabled: true, onSelect: () => onpick('disabled') },
		{ separator: true },
		{ label: 'Remove', tone: 'danger', onSelect: () => onpick('remove') }
	]}
>
	{#snippet trigger(props)}
		<IconButton {...props} label="More actions for silo-web" icon={EllipsisVertical} />
	{/snippet}
</Menu>

<SplitButton
	label="Deploy"
	menuLabel="More deploy options"
	onclick={() => onpick('deploy')}
	items={[
		{ label: 'Deploy with pull', onSelect: () => onpick('pull') },
		{ label: 'Build and deploy', onSelect: () => onpick('build') }
	]}
/>
