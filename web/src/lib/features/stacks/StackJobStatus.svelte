<script lang="ts">
	// A stack's running job in its list row, in one line: a spinner and what
	// runs ("Deploying"), linked to the job page. The list finds the job in
	// the running list (list-jobs.ts), so it shows again after a reload.
	import type { Job } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { Spinner } from '$lib/ui';
	import { runningHint, runningLabel } from './list-jobs';

	let { job }: { job: Pick<Job, 'id' | 'kind' | 'state'> } = $props();
</script>

<a class="running" href={routes.job(job.id)} title={runningHint(job)} aria-busy="true">
	<Spinner size={14} />
	<span>{runningLabel(job)}</span>
</a>

<style>
	.running {
		position: relative;
		z-index: 2;
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		max-width: 100%;
		color: var(--accent-text);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
		text-decoration: none;
		white-space: nowrap;
	}

	.running span {
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.running:hover span {
		text-decoration: underline;
	}
</style>
