<script lang="ts">
	// Test harness: a LinksEditor over state rows, showing the links it
	// would save (cleanLinks) and whether they can be saved.
	import { untrack } from 'svelte';
	import LinksEditor from '$lib/features/common/LinksEditor.svelte';
	import {
		cleanLinks,
		linkRows,
		linksValid,
		type LinkRowProblem,
		type WebLink
	} from '$lib/features/common/links';

	let {
		initial = [],
		showAll = false,
		serverProblems = null
	}: {
		initial?: WebLink[];
		showAll?: boolean;
		serverProblems?: { rows: LinkRowProblem[]; list: string | null } | null;
	} = $props();
	let rows = $state(linkRows(untrack(() => initial)));
</script>

<LinksEditor bind:rows {showAll} {serverProblems} />
<p data-testid="links">{JSON.stringify(cleanLinks(rows))}</p>
<p data-testid="valid">{linksValid(rows) ? 'valid' : 'invalid'}</p>
