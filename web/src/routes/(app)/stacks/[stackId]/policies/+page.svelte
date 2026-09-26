<script lang="ts">
	// Policies (#22 stack tab): the stack's update policy (#20) and the
	// backup policies that include it (#10).
	import BackupPoliciesCard from '$lib/features/stacks/BackupPoliciesCard.svelte';
	import { useStackPage } from '$lib/features/stacks/context';
	import UpdatePolicyCard from '$lib/features/stacks/UpdatePolicyCard.svelte';

	const ctx = useStackPage();
	const stack = $derived(ctx.stack!);
</script>

<UpdatePolicyCard {stack} />
<BackupPoliciesCard {stack} />
