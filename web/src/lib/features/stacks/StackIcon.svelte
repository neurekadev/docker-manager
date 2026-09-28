<script lang="ts">
	// A stack's icon (#7, template registry): the current image of the
	// template it was created from (while its registry is known and the
	// template has one), else the stack tile of RESOURCE_ICONS. Stacks have
	// no icon of their own.
	import { createQuery } from '@tanstack/svelte-query';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import TemplateIcon from '$lib/features/templates/TemplateIcon.svelte';
	import { templateIconUrl, templateIconsQuery } from '$lib/features/templates/queries';
	import { IconTile } from '$lib/ui';
	import type { Stack } from './queries';

	interface Props {
		stack: Pick<Stack, 'template'>;
		size?: 'xs' | 'sm' | 'md' | 'lg';
	}

	let { stack, size = 'md' }: Props = $props();
	const icons = createQuery(() => ({ ...templateIconsQuery(), enabled: !!stack.template }));
	const url = $derived(templateIconUrl(icons.data, stack.template));
	const tile = resourceIcon('stack');
</script>

{#if url}
	<TemplateIcon {url} {size} fallback={tile} />
{:else}
	<IconTile {...tile} {size} />
{/if}
