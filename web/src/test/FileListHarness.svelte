<script lang="ts">
	// FileList test harness (#15): owns the bindable selection and records
	// what the list reports.
	import FileList from '$lib/features/files/FileList.svelte';
	import type { FileEntry, ListSort } from '$lib/features/files/api';
	import type { FileCommand } from '$lib/features/files/keyboard';
	import { EMPTY, type Selection } from '$lib/features/files/selection';

	interface Props {
		rows: FileEntry[];
		dir?: string;
		events: string[];
		cut?: string[];
		details?: boolean;
	}

	let { rows, dir = 'config', events, cut = [], details = false }: Props = $props();
	let selection = $state<Selection>(EMPTY);
	let sort = $state<ListSort>('type');
</script>

<input aria-label="Other field" />
<FileList
	bind:selection
	{rows}
	{dir}
	showParent={dir !== '.'}
	cut={new Set(cut)}
	{sort}
	onsort={(s) => {
		sort = s;
		events.push(`sort:${s}`);
	}}
	label="Files in config"
	onopen={(e, via) => events.push(`open:${e.path}:${via}`)}
	onparent={() => events.push('parent')}
	oncommand={(c: FileCommand) => events.push(`command:${c.kind}`)}
	dragScope="stack:s1"
	canMove
	canCopy
	{details}
/>
<output data-testid="selected">{selection.selected.join(',')}</output>
<output data-testid="cursor">{selection.cursor ?? ''}</output>
