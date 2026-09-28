<script lang="ts">
	// The top bar's running jobs (#22, docs/internal/web.md "Job progress
	// after reload"): "3 running" linking to the jobs list filtered to jobs
	// in progress, hidden while none runs. Fed by the running-jobs list
	// (activeJobsQuery, refreshed by job events), so it holds across reloads.
	import { createQuery } from '@tanstack/svelte-query';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import { activeJobsQuery, ACTIVE_JOBS_LIMIT } from '$lib/api/queries';
	import { routes } from '$lib/routes';

	interface Props {
		/** Load the list only while this holds (restricted users have no jobs view). */
		enabled?: boolean;
		/** Only the icon and the count (phones). */
		compact?: boolean;
	}

	let { enabled = true, compact = false }: Props = $props();
	const active = createQuery(() => ({ ...activeJobsQuery(), enabled }));
	const count = $derived(enabled ? (active.data?.length ?? 0) : 0);
	const text = $derived(count >= ACTIVE_JOBS_LIMIT ? `${ACTIVE_JOBS_LIMIT}+` : String(count));
	const label = $derived(
		`${text} ${count === 1 ? 'job' : 'jobs'} running. Open the jobs in progress`
	);
</script>

{#if count > 0}
	<a
		class="running"
		href={routes.jobs(undefined, { state: 'active' })}
		aria-label={label}
		title={label}
	>
		<LoaderCircle class="spin" size={14} strokeWidth={2} aria-hidden="true" />
		<span class="num" aria-hidden="true">{text}{compact ? '' : ' running'}</span>
	</a>
{/if}

<style>
	.running {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		min-height: 28px;
		padding: 0 var(--space-2);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-full);
		background: var(--surface-raised);
		color: var(--text-default);
		font-size: var(--text-caption);
		text-decoration: none;
		white-space: nowrap;
	}

	.running:hover {
		border-color: var(--border-strong);
		color: var(--text-strong);
	}

	.running :global(.spin) {
		color: var(--accent);
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.running :global(.spin) {
			animation: none;
		}
	}
</style>
