<script lang="ts">
	// A template's icon (template registry): preview, upload (PNG, JPEG, GIF,
	// WebP or SVG up to 256 KiB; the server checks the bytes) and removal.
	// Stacks created from the template show the new icon right away.
	import { useQueryClient } from '@tanstack/svelte-query';
	import ImageUp from '@lucide/svelte/icons/image-up';
	import { Button, Notice, errorMessage, toast } from '$lib/ui';
	import { fileToBase64, removeIcon, setIcon } from './actions';
	import { templateKeys, type Template } from './queries';
	import TemplateIcon from './TemplateIcon.svelte';

	let { template }: { template: Template } = $props();
	const queryClient = useQueryClient();
	let input = $state<HTMLInputElement | null>(null);
	let busy = $state(false);
	let error = $state<string | null>(null);

	const MAX = 256 * 1024;

	async function upload(file: File) {
		error = null;
		if (file.size > MAX) {
			error = `${file.name} is larger than 256 KiB. Use a smaller image.`;
			return;
		}
		busy = true;
		try {
			const next = await setIcon(template, await fileToBase64(file));
			queryClient.setQueryData(templateKeys.detail(template.id), next);
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			toast.success(`Changed the icon of ${template.name}`);
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = false;
			if (input) input.value = '';
		}
	}

	async function remove() {
		busy = true;
		error = null;
		try {
			const next = await removeIcon(template);
			queryClient.setQueryData(templateKeys.detail(template.id), next);
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			toast.success(`Removed the icon of ${template.name}`);
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = false;
		}
	}
</script>

<div class="icon">
	<TemplateIcon url={template.icon?.url} size="lg" />
	<div class="body">
		<p class="muted">
			PNG, JPEG, GIF, WebP or SVG, up to 256 KiB and 1024 × 1024 pixels. A square image with
			some transparent margin looks best.
		</p>
		<div class="buttons">
			<input
				bind:this={input}
				type="file"
				accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml"
				class="sr-only"
				tabindex="-1"
				aria-hidden="true"
				onchange={(e) => {
					const f = e.currentTarget.files?.[0];
					if (f) void upload(f);
				}}
			/>
			<Button icon={ImageUp} loading={busy} onclick={() => input?.click()}
				>{template.icon ? 'Replace icon' : 'Upload icon'}</Button
			>
			{#if template.icon}
				<Button variant="ghost" disabled={busy} onclick={() => void remove()}
					>Remove icon</Button
				>
			{/if}
		</div>
		{#if error}
			<Notice tone="danger" live="alert" title="The icon was not changed">{error}</Notice>
		{/if}
	</div>
</div>

<style>
	.icon {
		display: flex;
		align-items: flex-start;
		gap: var(--space-4);
	}

	.body {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		min-width: 0;
	}

	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}
</style>
