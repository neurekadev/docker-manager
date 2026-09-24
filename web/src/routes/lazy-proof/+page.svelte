<script lang="ts">
	// TEMPORARY proof page (#11): shows that CodeMirror, ECharts and xterm.js
	// load lazily and that Bits UI and Lucide work with the pinned Svelte.
	// No design intent; remove it once real views use these libraries (#22).
	import { onDestroy } from 'svelte';
	import { ContextMenu } from 'bits-ui';
	import FileText from '@lucide/svelte/icons/file-text';
	import { mountLineChart, mountTerminal, mountYamlEditor, type Mounted } from '$lib/lazy';

	let editorEl = $state<HTMLElement>();
	let chartEl = $state<HTMLElement>();
	let terminalEl = $state<HTMLElement>();
	let loaded = $state<string[]>([]);
	let selected = $state<string | null>(null);
	const mounted: Mounted[] = [];

	async function load(kind: 'editor' | 'chart' | 'terminal') {
		if (loaded.includes(kind)) return;
		if (kind === 'editor' && editorEl) {
			mounted.push(await mountYamlEditor(editorEl, 'services:\n  web:\n    image: nginx\n'));
		} else if (kind === 'chart' && chartEl) {
			const now = Date.UTC(2026, 0, 1);
			const points = Array.from({ length: 12 }, (_, i) => ({
				at: new Date(now + i * 60_000),
				value: (i * 7) % 10
			}));
			mounted.push(await mountLineChart(chartEl, 'cpu', points));
		} else if (kind === 'terminal' && terminalEl) {
			const term = await mountTerminal(terminalEl);
			term.write('DockYard terminal proof\n');
			mounted.push(term);
		}
		loaded = [...loaded, kind];
	}

	onDestroy(() => mounted.forEach((m) => m.destroy()));
</script>

<svelte:head>
	<title>Lazy-load proof · DockYard</title>
</svelte:head>

<main>
	<h1>Lazy-load proof</h1>
	<p data-testid="loaded">Loaded: {loaded.length ? loaded.join(', ') : 'none'}</p>

	<button type="button" onclick={() => load('editor')}>Load editor</button>
	<div data-testid="editor" bind:this={editorEl}></div>

	<button type="button" onclick={() => load('chart')}>Load chart</button>
	<div data-testid="chart" bind:this={chartEl} style="width: 480px; height: 200px"></div>

	<button type="button" onclick={() => load('terminal')}>Load terminal</button>
	<div data-testid="terminal" bind:this={terminalEl}></div>

	<ContextMenu.Root>
		<ContextMenu.Trigger data-testid="context-target">
			<FileText aria-hidden="true" /> compose.yaml (right-click)
		</ContextMenu.Trigger>
		<ContextMenu.Portal>
			<ContextMenu.Content>
				<ContextMenu.Item onSelect={() => (selected = 'Rename')}>Rename</ContextMenu.Item>
				<ContextMenu.Item onSelect={() => (selected = 'Download')}
					>Download</ContextMenu.Item
				>
			</ContextMenu.Content>
		</ContextMenu.Portal>
	</ContextMenu.Root>
	<p data-testid="selected">Selected: {selected ?? 'nothing'}</p>
</main>
