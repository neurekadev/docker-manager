<script lang="ts">
	// Backups (#10 stack tab): the stack's backups, restorable whole (the
	// project directory and its volumes, then optionally a deploy) or file
	// by file from a picker.
	import BackupsTab from '$lib/features/backups/BackupsTab.svelte';
	import { useStackPage } from '$lib/features/stacks/context';
	import { stackTitle } from '$lib/features/stacks/model';

	const ctx = useStackPage();
	const stack = $derived(ctx.stack!);
</script>

<BackupsTab
	filter={{ stackId: ctx.id, kind: 'stack' }}
	subject={stackTitle(stack)}
	canDeploy={stack.actions.includes('stack.deploy')}
/>
