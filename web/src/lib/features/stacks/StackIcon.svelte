<script lang="ts">
	// A stack's icon (#7, template registry): the icon chosen for the stack,
	// else the current icon of the template it was created from (while its
	// registry is known), else the default stack glyph.
	import { createQuery } from '@tanstack/svelte-query';
	import { serviceIcon } from '$lib/design/icons';
	import TemplateIcon from '$lib/features/templates/TemplateIcon.svelte';
	import { templateIconUrl, templateIconsQuery } from '$lib/features/templates/queries';
	import { IconTile } from '$lib/ui';
	import { stackIcon } from './model';
	import type { Stack } from './queries';

	interface Props {
		stack: Pick<Stack, 'icon' | 'template'>;
		size?: 'sm' | 'md' | 'lg';
	}

	let { stack, size = 'md' }: Props = $props();
	const icons = createQuery(() => ({ ...templateIconsQuery(), enabled: !!stack.template }));
	const url = $derived(stack.icon ? undefined : templateIconUrl(icons.data, stack.template));
	const glyph = $derived(stackIcon(stack));
</script>

{#if url}
	<TemplateIcon {url} {size} />
{:else}
	<IconTile icon={serviceIcon(glyph.icon)} color={glyph.color} {size} />
{/if}
