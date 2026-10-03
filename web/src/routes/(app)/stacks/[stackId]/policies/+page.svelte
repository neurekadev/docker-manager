<script lang="ts">
	// Policies (#22 stack tab): whether the update settings (#20) and the
	// backup settings (#10) cover the stack.
	import BackupCoverageCard from '$lib/features/stacks/BackupCoverageCard.svelte';
	import { useStackPage } from '$lib/features/stacks/context';
	import UpdatePolicyCard from '$lib/features/stacks/UpdatePolicyCard.svelte';

	const ctx = useStackPage();
	const stack = $derived(ctx.stack!);
</script>

<UpdatePolicyCard {stack} />
<BackupCoverageCard {stack} />
