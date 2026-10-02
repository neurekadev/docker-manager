<script lang="ts">
	// The log viewer in its own window (#22): /popout/logs?stack=<id> or
	// ?environment=<id>&container=<name>. Same viewer, the whole window.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { stackQuery } from '$lib/features/files/resources';
	import LogPanel, { type LogTarget } from '$lib/features/logs/LogPanel.svelte';
	import { EmptyState } from '$lib/ui';

	const q = page.url.searchParams;
	const target: LogTarget | null = q.get('stack')
		? { kind: 'stack', stackId: q.get('stack')! }
		: q.get('environment') && q.get('container')
			? {
					kind: 'container',
					environmentId: q.get('environment')!,
					containerId: q.get('container')!
				}
			: null;

	const stack = createQuery(() => ({
		...stackQuery(target?.kind === 'stack' ? target.stackId : ''),
		enabled: target?.kind === 'stack'
	}));
	const name = $derived(
		target?.kind === 'stack'
			? stack.data?.displayName || stack.data?.name || 'Stack'
			: target?.kind === 'container'
				? target.containerId
				: ''
	);

	$effect(() => {
		document.title = name ? `${name} Logs · Docker Manager` : 'Logs · Docker Manager';
	});
</script>

{#if target}
	<h1 class="sr-only">Logs of {name}</h1>
	<LogPanel {target} {name} popout={false} />
{:else}
	<EmptyState
		level={1}
		title="Nothing to Show"
		description="Open the logs from a stack or container page, then choose Open in a New Window."
	/>
{/if}
