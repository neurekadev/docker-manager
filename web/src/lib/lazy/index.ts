// Lazy entry points for the heavy UI libraries (#11,
// docs/adr/0002-frontend-libraries.md). Each library is reachable ONLY
// through a dynamic import() in these functions, so it is split into its
// own chunks and fetched when a view first needs it. The build check
// (scripts/verify-build.mjs) fails if any of them lands in a statically
// imported chunk. Views import these helpers, never the libraries.

export interface Mounted {
	destroy(): void;
}

export interface YamlEditor extends Mounted {
	text(): string;
}

/** CodeMirror 6 with YAML highlighting (stack and volume file editing, #7/#15). */
export async function mountYamlEditor(parent: HTMLElement, doc: string): Promise<YamlEditor> {
	const [{ EditorView, basicSetup }, { yaml }] = await Promise.all([
		import('codemirror'),
		import('@codemirror/lang-yaml')
	]);
	const view = new EditorView({ doc, extensions: [basicSetup, yaml()], parent });
	return { destroy: () => view.destroy(), text: () => view.state.doc.toString() };
}

export interface SeriesPoint {
	at: Date;
	value: number;
}

/** Apache ECharts line chart, tree-shaken to the parts in ./echarts.ts (host metrics, #5). */
export async function mountLineChart(
	el: HTMLElement,
	name: string,
	points: SeriesPoint[]
): Promise<Mounted> {
	const { init } = await import('./echarts');
	const chart = init(el, undefined, { renderer: 'canvas' });
	chart.setOption({
		animation: false,
		tooltip: { trigger: 'axis' },
		xAxis: { type: 'time' },
		yAxis: { type: 'value' },
		series: [
			{
				type: 'line',
				name,
				showSymbol: false,
				data: points.map((p) => [p.at.getTime(), p.value])
			}
		]
	});
	return { destroy: () => chart.dispose() };
}

export interface TerminalHandle extends Mounted {
	write(data: string): void;
}

/** xterm.js terminal (container exec, #19). */
export async function mountTerminal(el: HTMLElement): Promise<TerminalHandle> {
	const [{ Terminal }] = await Promise.all([
		import('@xterm/xterm'),
		import('@xterm/xterm/css/xterm.css')
	]);
	const term = new Terminal({ convertEol: true, disableStdin: true, rows: 8 });
	term.open(el);
	return { destroy: () => term.dispose(), write: (d) => term.write(d) };
}
