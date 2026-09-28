<script lang="ts">
	// The stack header's Update (#20): names the services with a newer image
	// (image and tag, never digests), then pulls every image and redeploys
	// in one deploy job (tracked in the page's tray, "Updated Silo"). With no
	// newer image known it says the images are up to date and still lets the
	// user pull and redeploy. Schedules and automatic updates live in the
	// update policy (the Policies tab).
	import { useQueryClient } from '@tanstack/svelte-query';
	import { routes } from '$lib/routes';
	import { ConfirmDialog, formatDateTime, formatRelative } from '$lib/ui';
	import { startUpdate } from './deploy.svelte';
	import { lastUpdateCheck, pendingUpdates, stackTitle } from './model';
	import type { Stack, StackImageStatus } from './queries';
	import type { JobTray } from './tray.svelte';

	interface Props {
		open: boolean;
		stack: Stack;
		images?: StackImageStatus[];
		tray: JobTray;
		/** Show the link to the Policies tab. */
		policiesLink?: boolean;
	}

	let { open = $bindable(false), stack, images, tray, policiesLink = true }: Props = $props();
	const queryClient = useQueryClient();
	const title = $derived(stackTitle(stack));
	const pending = $derived(pendingUpdates(images));
	const checked = $derived(lastUpdateCheck(images));
</script>

<ConfirmDialog
	bind:open
	title={pending.length ? `Update ${title}?` : `${title} is up to date`}
	message={pending.length
		? `Downloads the newer ${pending.length === 1 ? 'image' : 'images'} and recreates the ${pending.length === 1 ? 'service' : 'services'} that use ${pending.length === 1 ? 'it' : 'them'}.`
		: `No newer images are known for ${title}. Update still downloads every image and recreates the services whose image changed.`}
	confirmLabel="Update"
	size="md"
	onconfirm={() => startUpdate(stack, tray, queryClient)}
>
	{#if pending.length}
		<ul class="images" aria-label="Newer images">
			{#each pending as p (p.service)}
				<li>
					<span class="svc">{p.service}</span>
					<span class="mono image">{p.image}</span>
					{#if p.pulled}<span class="muted">Already downloaded</span>{/if}
				</li>
			{/each}
		</ul>
	{/if}
	<p class="foot muted">
		{#if checked}
			Last checked <time datetime={checked} title={formatDateTime(checked)}
				>{formatRelative(checked)}</time
			>.
		{/if}
		{#if policiesLink}
			<a href={routes.stack(stack.id, 'policies')}>Update policy</a>
		{/if}
	</p>
</ConfirmDialog>

<style>
	.images {
		display: grid;
		gap: var(--space-1);
		margin: var(--space-3) 0 0;
		padding: 0;
		list-style: none;
	}

	.images li {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-2);
		min-width: 0;
	}

	.svc {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.image {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.foot {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-3);
		margin-top: var(--space-3);
		font-size: var(--text-caption);
	}

	.foot a {
		color: var(--accent-text);
	}
</style>
