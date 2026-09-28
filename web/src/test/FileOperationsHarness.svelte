<script lang="ts">
	// Test harness: the file manager's operations panel for one root, with
	// the root's tracked file jobs (useTrackedJobs + fileJobMatch, as
	// FileManager wires them). Render it through QueryHarness.
	import type { Job } from '$lib/api/client';
	import type { FileScope } from '$lib/features/files/api';
	import { fileJobMatch, fileJobTitle } from '$lib/features/files/jobs';
	import OperationsPanel from '$lib/features/files/OperationsPanel.svelte';
	import { UploadQueue } from '$lib/features/files/uploads.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';

	let {
		scope,
		rootLabel = 'root',
		onfinish = () => {}
	}: {
		scope: FileScope;
		rootLabel?: string;
		onfinish?: (job: Job, title: string) => void;
	} = $props();

	const jobs = useTrackedJobs(() => fileJobMatch(scope));
	const uploads = new UploadQueue({ url: () => '/upload', unload: null });
</script>

<OperationsPanel
	{uploads}
	{jobs}
	titleOf={(job) => fileJobTitle(job, scope, rootLabel)}
	{onfinish}
/>
