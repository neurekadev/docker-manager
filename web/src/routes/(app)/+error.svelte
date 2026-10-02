<script lang="ts">
	// Router errors inside the signed-in area (#22): rendered within the
	// (app) layout, so the sidebar and top bar stay and the user can move
	// on from there. Everything else uses routes/+error.svelte.
	import { page } from '$app/state';
	import ErrorPageBody from '$lib/features/common/ErrorPageBody.svelte';
	import { usePage } from '$lib/shell/page.svelte';

	const title = $derived(page.status === 404 ? 'Page Not Found' : 'Error');
	usePage(() => ({ title, crumbs: [{ label: title }] }));
</script>

<ErrorPageBody status={page.status} />
