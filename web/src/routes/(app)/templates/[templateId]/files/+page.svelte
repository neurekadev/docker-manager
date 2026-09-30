<script lang="ts">
	// Template detail, Files tab (template registry): the shared file manager
	// over the template's draft (compose.yaml, .env and the files next to
	// them). The draft lives on the manager, so it is always writable;
	// publishing a version freezes it.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import FileManager from '$lib/features/files/FileManager.svelte';
	import { fillViewport } from '$lib/features/files/fill';
	import { templateQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';

	const id = $derived(page.params.templateId ?? '');
	const template = createQuery(() => templateQuery(id));
	const t = $derived(template.data);

	usePage(() => ({
		title: `${t?.name ?? 'Template'} files`,
		crumbs: [
			{ label: 'Templates', href: routes.templates() },
			{ label: t?.name ?? 'Template', href: routes.template(id) },
			{ label: 'Files' }
		]
	}));
</script>

{#if t}
	<div class="page" use:fillViewport={{ min: 520 }}>
		{#key t.id}
			<FileManager
				scope={{ kind: 'template', templateId: t.id }}
				rootLabel={t.name}
				capabilities={t.actions}
				label="Files of the template {t.name}"
				title="Draft files"
			/>
		{/key}
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		min-height: 520px;
	}
</style>
