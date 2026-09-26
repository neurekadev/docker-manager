<script lang="ts">
	// Stack migration (#35): the wizard, under the stack's header (the
	// layout sets the title and crumbs).
	import { routes } from '$lib/routes';
	import { useStackPage } from '$lib/features/stacks/context';
	import MigrationWizard from '$lib/features/stacks/MigrationWizard.svelte';
	import { stackTitle } from '$lib/features/stacks/model';
	import { Button, Card } from '$lib/ui';

	const ctx = useStackPage();
	const stack = $derived(ctx.stack!);
	const title = $derived(stackTitle(stack));
</script>

<Card title="Migrate {title} to another environment" id="migrate">
	{#if stack.actions.includes('stack.migrate')}
		{#key ctx.id}<MigrationWizard {stack} tray={ctx.tray} />{/key}
	{:else}
		<p class="muted">
			Migrating needs the permission to migrate this stack. Ask the owner of this Docker
			Manager.
		</p>
		<Button href={routes.stack(ctx.id)}>Back to {title}</Button>
	{/if}
</Card>
