<script lang="ts">
	// Container logs (#8): follow one container's output, full height.
	// A child route of the container detail; works on its own too.
	import { page } from '$app/state';
	import { fillViewport } from '$lib/features/files/fill';
	import LogPanel from '$lib/features/logs/LogPanel.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';

	const environmentId = $derived(page.params.environmentId ?? '');
	const containerId = $derived(page.params.containerId ?? '');

	usePage(() => ({
		title: `${containerId} logs`,
		crumbs: [
			{ label: 'Containers', href: routes.containers() },
			{ label: containerId, href: routes.container(environmentId, containerId) },
			{ label: 'Logs' }
		],
		environmentScoped: true
	}));
</script>

<div class="page" use:fillViewport={{ bottom: 24, min: 420 }}>
	{#key `${environmentId}/${containerId}`}
		<LogPanel target={{ kind: 'container', environmentId, containerId }} name={containerId} />
	{/key}
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		min-height: 420px;
	}
</style>
