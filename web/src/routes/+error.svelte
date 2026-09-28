<script lang="ts">
	// Router errors outside the app shell (#22): unknown addresses and
	// failures of public pages or of the shell itself. Errors inside the
	// signed-in area render in routes/(app)/+error.svelte, within the shell.
	import { page } from '$app/state';
	import ErrorPageBody from '$lib/features/common/ErrorPageBody.svelte';
	import Logo from '$lib/shell/Logo.svelte';
</script>

<div class="error-page">
	<Logo />
	<ErrorPageBody status={page.status} />
</div>

<style>
	.error-page {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-4);
		min-height: 100dvh;
		padding: var(--space-6);
	}
</style>
