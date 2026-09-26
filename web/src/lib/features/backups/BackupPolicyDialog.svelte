<script lang="ts">
	// Create or edit a backup policy (#10) in a dialog: the setup wizard over
	// the Backups pages instead of a page of its own. Nothing is saved before
	// the wizard's last step, so closing half-way leaves no policy behind.
	// Render it only while open ({#if}).
	import { goto } from '$app/navigation';
	import { routes } from '$lib/routes';
	import { Dialog } from '$lib/ui';
	import PolicyWizard from './PolicyWizard.svelte';
	import type { BackupPolicy } from './model';

	let {
		open = $bindable(true),
		policy,
		owner
	}: { open?: boolean; policy?: BackupPolicy; owner: boolean } = $props();

	async function done(saved: BackupPolicy) {
		// A new policy opens its page (the dialog goes with this one).
		if (policy) open = false;
		else await goto(routes.backupPolicy(saved.id));
	}
</script>

<Dialog
	bind:open
	title={policy ? `Edit ${policy.name}` : 'Create backup policy'}
	description="Choose what to back up, how, when and for how long. Nothing runs until you start it or turn its schedule on."
	size="xl"
>
	<PolicyWizard {policy} {owner} ondone={done} />
</Dialog>
