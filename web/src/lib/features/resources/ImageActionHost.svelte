<script lang="ts">
	// Tag and remove images (#6). Tagging is a short request (an existing
	// tag moves to this image); removal is a job, refused while containers
	// use the image or when it is Docker Manager's own (#32), with the server's
	// removal preview shown first.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap, type Job, type Schema } from '$lib/api/client';
	import { imageQuery, queryKeys, type Image } from '$lib/api/queries';
	import { Button, Dialog, TextField, fieldError, toast } from '$lib/ui';
	import RemovalDialog from './RemovalDialog.svelte';
	import { trackJob } from './jobs.svelte';
	import { removeImage } from './object-actions';
	import { shortDigest } from './model';
	import { refusal, RefusalError, type Refusal } from './refusals';

	interface Props {
		environmentName?: (env: string) => string | undefined;
		onremoved?: (im: Image) => void;
		/** Called with the removal's job (the page shows its progress). */
		onstarted?: (job: Job) => void;
	}

	let { environmentName, onremoved, onstarted }: Props = $props();
	const queryClient = useQueryClient();

	let target = $state<Image | null>(null);
	let removal = $state<Schema<'Removal'> | undefined>(undefined);
	let removeOpen = $state(false);
	let tagOpen = $state(false);
	let repository = $state('');
	let tag = $state('');
	let tagBusy = $state(false);
	let tagFailure = $state<{ cause: unknown; refusal: Refusal } | null>(null);

	const label = (im: Image) => im.repoTags[0] ?? shortDigest(im.id);

	export async function request(im: Image, action: 'tag' | 'remove') {
		target = im;
		if (action === 'tag') {
			const first = im.repoTags[0] ?? '';
			const colon = first.lastIndexOf(':');
			repository = colon > first.lastIndexOf('/') ? first.slice(0, colon) : first;
			tag = '';
			tagFailure = null;
			tagOpen = true;
			return;
		}
		let full = im;
		try {
			full = await queryClient.fetchQuery(imageQuery(im.environmentId, im.id));
		} catch {
			// The server still decides.
		}
		target = full;
		removal = full.details?.removal;
		removeOpen = true;
	}

	async function remove() {
		const im = target!;
		const ctx = {
			kind: 'image' as const,
			name: label(im),
			verb: 'remove' as const,
			protection: im.protection,
			environmentName: environmentName?.(im.environmentId)
		};
		try {
			const job = await removeImage(im.environmentId, im.id, {
				force: im.repoTags.length > 1
			});
			onstarted?.(job);
			trackJob(job, {
				ctx,
				queryClient,
				invalidate: [queryKeys.images.all],
				onfinish: (j) => {
					if (j.state === 'succeeded') onremoved?.(im);
				}
			});
		} catch (e) {
			throw new RefusalError(refusal(e, ctx));
		}
	}

	async function saveTag() {
		const im = target!;
		tagBusy = true;
		tagFailure = null;
		const ref = `${repository.trim()}:${tag.trim() || 'latest'}`;
		try {
			await unwrap(
				api.POST('/api/v1/environments/{environmentId}/images/{imageId}/tags', {
					params: { path: { environmentId: im.environmentId, imageId: im.id } },
					body: { repository: repository.trim(), tag: tag.trim() || undefined }
				})
			);
			toast.success(`Tagged ${ref}`);
			void queryClient.invalidateQueries({ queryKey: queryKeys.images.all });
			tagOpen = false;
		} catch (e) {
			tagFailure = {
				cause: e,
				refusal: refusal(e, {
					kind: 'image',
					name: label(im),
					verb: 'tag',
					environmentName: environmentName?.(im.environmentId)
				})
			};
		} finally {
			tagBusy = false;
		}
	}
</script>

{#if target}
	<RemovalDialog
		bind:open={removeOpen}
		kind="image"
		name={label(target)}
		{removal}
		affected={target.repoTags.length
			? target.repoTags.map((t) => ({ label: t, detail: 'tag' }))
			: [{ label: shortDigest(target.id), detail: 'untagged image' }]}
		confirmLabel="Remove Image"
		onconfirm={remove}
	/>

	<Dialog bind:open={tagOpen} title="Tag {label(target)}" size="sm">
		<form
			class="form"
			id="tag-form"
			onsubmit={(e) => {
				e.preventDefault();
				void saveTag();
			}}
		>
			<TextField
				label="Repository"
				mono
				required
				bind:value={repository}
				placeholder="registry.example.com/team/app"
				error={fieldError(tagFailure?.cause, 'body.repository')}
			/>
			<TextField
				label="Tag"
				mono
				bind:value={tag}
				placeholder="latest"
				description="Optional. An existing tag with this name moves to this image."
				error={fieldError(tagFailure?.cause, 'body.tag')}
			/>
			{#if tagFailure && !tagFailure.refusal.code?.startsWith('validation')}
				<p class="error" role="alert">
					{tagFailure.refusal.title}
					{tagFailure.refusal.body ?? ''}
				</p>
			{/if}
		</form>
		{#snippet footer()}
			<Button variant="ghost" onclick={() => (tagOpen = false)} disabled={tagBusy}
				>Cancel</Button
			>
			<Button
				type="submit"
				form="tag-form"
				variant="primary"
				loading={tagBusy}
				disabled={!repository.trim()}>Tag Image</Button
			>
		{/snippet}
	</Dialog>
{/if}

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
