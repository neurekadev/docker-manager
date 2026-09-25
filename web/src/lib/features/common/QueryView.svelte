<script lang="ts" generics="T">
	// The four states of a loaded region (#22): loading (Skeleton in an
	// aria-busy region), denied (403: DeniedState), missing (404), failed
	// (ErrorState with Retry) and the content. Pages wrap each query with it
	// so no screen forgets a state.
	import type { Snippet } from 'svelte';
	import DeniedState from '$lib/ui/DeniedState.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorState from '$lib/ui/ErrorState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import SearchX from '@lucide/svelte/icons/search-x';
	import { isDenied, isNotFound } from './access';

	interface QueryLike<D> {
		data: D | undefined;
		error: unknown;
		isPending: boolean;
		isError: boolean;
		isRefetching?: boolean;
		refetch: () => unknown;
	}

	interface Props {
		query: QueryLike<T>;
		/** What failed, e.g. "The update policies could not be loaded." */
		errorTitle: string;
		deniedTitle?: string;
		deniedDescription?: string;
		notFoundTitle?: string;
		notFoundDescription?: string;
		lines?: number;
		children: Snippet<[T]>;
	}

	let {
		query,
		errorTitle,
		deniedTitle = "You don't have access to this.",
		deniedDescription = 'Ask the owner of this DockYard to grant it.',
		notFoundTitle = 'Not found.',
		notFoundDescription = 'It was deleted, or you no longer have access to it.',
		lines = 4,
		children
	}: Props = $props();
</script>

{#if query.isPending}
	<div class="loading" aria-busy="true">
		<Skeleton {lines} height="20px" />
	</div>
{:else if query.isError && isDenied(query.error)}
	<DeniedState title={deniedTitle} description={deniedDescription} level={2} />
{:else if query.isError && isNotFound(query.error)}
	<EmptyState
		icon={SearchX}
		color="slate"
		title={notFoundTitle}
		description={notFoundDescription}
		level={2}
	/>
{:else if query.isError}
	<ErrorState
		error={query.error}
		title={errorTitle}
		onretry={() => query.refetch()}
		retrying={query.isRefetching}
	/>
{:else if query.data !== undefined}
	{@render children(query.data)}
{/if}

<style>
	.loading {
		padding: var(--space-4);
	}
</style>
