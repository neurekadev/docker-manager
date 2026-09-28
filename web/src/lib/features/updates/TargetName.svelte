<script lang="ts">
	// An update target by its name (#20): a stack by its display name
	// (linked to its policies tab), a standalone container by its name
	// (linked to the container). Container targets are looked up in their
	// environment's containers, so an Engine ID is never shown.
	import { createQuery } from '@tanstack/svelte-query';
	import { routes } from '$lib/routes';
	import { containersQuery, stacksQuery } from '$lib/features/common/data';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { containerTargetName } from './model';

	interface Props {
		type: string;
		id: string;
		environmentId: string;
		/** Secondary line; default: what the target is. */
		sub?: string;
	}

	let { type, id, environmentId, sub }: Props = $props();

	const stacks = createQuery(() => ({ ...stacksQuery(), enabled: type === 'stack' }));
	const containers = createQuery(() => ({
		...containersQuery(environmentId),
		enabled: type === 'container' && !!environmentId
	}));

	const stack = $derived(type === 'stack' ? stacks.data?.find((s) => s.id === id) : undefined);
	const container = $derived(
		type === 'container' ? containerTargetName(id, containers.data) : undefined
	);
	const name = $derived(
		type === 'stack'
			? stack
				? stack.displayName || stack.name
				: stacks.isPending
					? 'Stack'
					: 'Deleted stack'
			: (container?.name ?? id)
	);
	const href = $derived(
		type === 'stack'
			? stack
				? routes.stack(id, 'policies')
				: undefined
			: container && container.name !== 'Removed container'
				? routes.container(environmentId, container.name)
				: undefined
	);
</script>

<NameCell {name} {href} sub={sub ?? (type === 'stack' ? 'Stack' : 'Standalone container')} />
