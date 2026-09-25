<script lang="ts">
	// Builds (#22 foundation placeholder; the feature workstream replaces it).
	import Hammer from '@lucide/svelte/icons/hammer';
	import SectionPlaceholder from '$lib/shell/SectionPlaceholder.svelte';
</script>

<SectionPlaceholder
	title="Builds"
	description="Image builds from Git repositories and Compose build sections."
	icon={Hammer}
	environmentScoped
/>
