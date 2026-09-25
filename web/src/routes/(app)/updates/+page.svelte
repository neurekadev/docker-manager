<script lang="ts">
	// Updates (#22 foundation placeholder; the feature workstream replaces it).
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import SectionPlaceholder from '$lib/shell/SectionPlaceholder.svelte';
</script>

<SectionPlaceholder
	title="Updates"
	description="Digest-based update checks and runs."
	icon={PackageCheck}
	environmentScoped
/>
