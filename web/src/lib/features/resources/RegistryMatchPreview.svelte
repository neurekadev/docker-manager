<script lang="ts">
	// Which registry connection an image reference will use (#19: shown
	// before a pull, deterministic; ambiguous matches need an explicit
	// choice; a revoked connection fails instead of pulling anonymously).
	// Needs registry.read; without it nothing is shown (the pull still
	// selects its connection on the server).
	import { createQuery } from '@tanstack/svelte-query';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { ApiRequestError } from '$lib/api/client';
	import { registryMatchQuery } from '$lib/api/queries';
	import { Notice, Select, Skeleton } from '$lib/ui';

	interface Props {
		reference: string;
		environmentId?: string;
		stackId?: string;
		/** The chosen connection when several tie (bindable). */
		selected?: string;
		/** Told whether a pull can go ahead (false: ambiguous without a choice, revoked). */
		onready?: (ready: boolean) => void;
	}

	let { reference, environmentId, stackId, selected = $bindable(''), onready }: Props = $props();

	// Debounce typing: ask the server 400 ms after the last keystroke.
	let debounced = $state('');
	$effect(() => {
		const r = reference.trim();
		const t = setTimeout(() => (debounced = r), 400);
		return () => clearTimeout(t);
	});

	const match = createQuery(() => ({
		...registryMatchQuery(debounced, {
			environmentId,
			stackId,
			registryId: selected || undefined
		}),
		enabled: debounced.length > 0
	}));
	const m = $derived(match.data);
	const hidden = $derived(
		match.error instanceof ApiRequestError &&
			(match.error.status === 403 || match.error.status === 404)
	);
	const invalidRef = $derived(
		match.error instanceof ApiRequestError && match.error.status === 422
	);

	$effect(() => {
		onready?.(!m || (m.selection !== 'revoked' && (m.selection !== 'ambiguous' || !!selected)));
	});
</script>

{#if debounced && !hidden}
	<div class="preview" aria-live="polite">
		{#if match.isPending}
			<Skeleton height="44px" radius="md" />
		{:else if invalidRef}
			<Notice tone="warn" title="This is not a valid image reference" live="none">
				Use repository[:tag], for example nginx:1.27 or ghcr.io/acme/app:1.4.
			</Notice>
		{:else if m?.selection === 'connection' && m.selected}
			<Notice tone="info" icon={KeyRound} title="Uses {m.selected.name}" live="none">
				{m.host}/{m.repository}{m.selected.repositoryPattern
					? `, matched by ${m.selected.repositoryPattern}`
					: ''}{m.selected.secret?.fingerprint
					? `, credential ${m.selected.secret.fingerprint}`
					: ''}. Only the pull on this environment gets the credential; nothing is stored
				on the host.
			</Notice>
		{:else if m?.selection === 'anonymous'}
			<Notice tone="info" title="Pulls Anonymously" live="none">
				No registry connection matches {m.host}/{m.repository}, so only public images work.
				{#if m.host === 'docker.io'}Docker Hub limits anonymous pulls per IP address; add a
					connection in Registries to pull with your account.{/if}
			</Notice>
		{:else if m?.selection === 'ambiguous'}
			<Notice tone="warn" title="Several registry connections match equally" live="none">
				Choose the connection this pull uses.
			</Notice>
		{:else if m?.selection === 'revoked'}
			<Notice tone="danger" title="The matching connection is revoked" live="none">
				{m.selected?.name ?? 'It'} has no credential any more, and Docker Manager never falls
				back to anonymous pulls. Rotate its credential in Registries, or remove it.
			</Notice>
		{/if}
		{#if m && (m.selection === 'ambiguous' || (m.candidates.length > 1 && selected))}
			<Select
				label="Registry Connection"
				bind:value={selected}
				options={[
					{ value: '', label: 'Choose a Connection' },
					...m.candidates.map((c) => ({
						value: c.connection.id,
						label: `${c.connection.name} (${c.binding === 'none' ? 'every environment' : `bound to this ${c.binding}`})`
					}))
				]}
			/>
		{/if}
	</div>
{/if}

<style>
	.preview {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}
</style>
