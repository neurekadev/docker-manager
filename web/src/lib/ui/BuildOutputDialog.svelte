<script lang="ts">
	// The Build Output dialog (#264; build-output.svelte.ts): opened by a
	// job panel's "Show Output", mounted once by the signed-in layout.
	import { buildOutput } from './build-output.svelte';
	import BuildOutputView from './BuildOutputView.svelte';
	import Dialog from './Dialog.svelte';
</script>

{#if buildOutput.shown}
	{@const shown = buildOutput.shown}
	<Dialog open title="{shown.title} Output" size="xl" onclose={() => buildOutput.close()}>
		{#key shown.jobId}
			<BuildOutputView jobId={shown.jobId} label="{shown.title} Output" />
		{/key}
	</Dialog>
{/if}
