<script lang="ts">
	// Stack migration (#35): the wizard, under the stack's header (the
	// layout sets the title and crumbs). With one environment the wizard
	// says that a second one is needed instead of showing its steps.
	import { routes } from '$lib/routes';
	import { useStackPage } from '$lib/features/stacks/context';
	import MigrationWizard from '$lib/features/stacks/MigrationWizard.svelte';
	import { stackTitle } from '$lib/features/stacks/model';
	import { Button, Card } from '$lib/ui';

	const ctx = useStackPage();
	const stack = $derived(ctx.stack!);
	const title = $derived(stackTitle(stack));
</script>

<Card title="Migrate {title} to Another Environment" id="migrate">
	{#if stack.actions.includes('stack.migrate')}
		{#key ctx.id}<MigrationWizard {stack} tray={ctx.tray} />{/key}
	{:else}
		<p class="muted">
			You can't migrate {title}. Ask the owner for the permission.
		</p>
		<Button href={routes.stack(ctx.id)}>Back to {title}</Button>
	{/if}
</Card>
