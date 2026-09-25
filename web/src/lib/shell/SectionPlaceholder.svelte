<script lang="ts">
	// A section whose screens are not built yet (#22 foundation): the page
	// exists so navigation, breadcrumbs and deep links work; its feature
	// workstream replaces this component with the real view.
	import type { IconComponent } from '$lib/design/icons';
	import { Card, EmptyState, PageHeader } from '$lib/ui';
	import { usePage } from './page.svelte';

	interface Props {
		title: string;
		description: string;
		icon: IconComponent;
		environmentScoped?: boolean;
	}

	let { title, description, icon, environmentScoped = false }: Props = $props();
	usePage(() => ({ title, crumbs: [{ label: title }], environmentScoped }));
</script>

<div class="page">
	<PageHeader {title} {description} />
	<Card>
		<EmptyState
			{icon}
			color="slate"
			title="Not in the web UI yet"
			description="The {title.toLowerCase()} screens come in a later update. The API already serves this area."
			level={2}
		/>
	</Card>
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-5);
	}
</style>
