<script lang="ts">
	// EditorPane test harness (#15): a QueryClient and an in-memory file API.
	import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
	import EditorPane from '$lib/features/files/EditorPane.svelte';
	import type { FilesApi } from '$lib/features/files/api';
	import type { EditorSession } from '$lib/features/files/editor.svelte';

	interface Props {
		files: FilesApi;
		session: EditorSession;
		canWrite?: boolean;
	}

	let { files, session, canWrite = true }: Props = $props();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
</script>

<QueryClientProvider {client}>
	<EditorPane
		{files}
		{session}
		{canWrite}
		stack={{ name: 'Silo', configFiles: [] }}
		ondownload={() => {}}
		onisdir={() => {}}
	/>
</QueryClientProvider>
