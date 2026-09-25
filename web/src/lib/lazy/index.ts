// Lazy entry points for the heavy UI libraries (#11,
// docs/adr/0002-frontend-libraries.md). Each library is reachable ONLY
// through a dynamic import() in these functions, so it is split into its
// own chunks and fetched when a view first needs it. The build check
// (scripts/verify-build.mjs) fails if any of them lands in a statically
// imported chunk. Views import these helpers (or the $lib/ui wrappers
// CodeEditor, Sparkline, TerminalView), never the libraries. Every mount
// applies DockYard's theme (#22): ./codemirror-theme.ts, ./echarts-theme.ts,
// TERMINAL_THEME in ./palette.ts.
import { CHART_COLORS, TERMINAL_THEME, EDITOR_COLORS } from './palette';

export interface Mounted {
	destroy(): void;
}

export interface YamlEditor extends Mounted {
	text(): string;
	/** Replaces the whole document (e.g. "Reload from disk"). */
	setText(text: string): void;
	focus(): void;
}

export interface EditorOptions {
	readOnly?: boolean;
	/** Called on every document change with the new text. */
	onChange?: (text: string) => void;
	/** Accessible name of the editing area (e.g. "compose.yaml"). */
	label?: string;
}

/** CodeMirror 6 with YAML highlighting (stack and volume file editing, #7/#15). */
export async function mountYamlEditor(
	parent: HTMLElement,
	doc: string,
	opts: EditorOptions = {}
): Promise<YamlEditor> {
	const [{ EditorView, basicSetup }, { yaml }, { EditorState }, { dockyardEditorTheme }] =
		await Promise.all([
			import('codemirror'),
			import('@codemirror/lang-yaml'),
			import('@codemirror/state'),
			import('./codemirror-theme')
		]);
	const extensions = [
		basicSetup,
		yaml(),
		dockyardEditorTheme,
		EditorState.readOnly.of(!!opts.readOnly),
		EditorView.contentAttributes.of({ 'aria-label': opts.label ?? 'Editor' }),
		EditorView.updateListener.of((u) => {
			if (u.docChanged) opts.onChange?.(u.state.doc.toString());
		})
	];
	const view = new EditorView({ doc, extensions, parent });
	return {
		destroy: () => view.destroy(),
		text: () => view.state.doc.toString(),
		setText: (text) =>
			view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }),
		focus: () => view.focus()
	};
}

export interface SeriesPoint {
	at: Date;
	/** null is a gap (no sample), rendered as a break, never as zero (#5). */
	value: number | null;
}

export interface Series {
	name: string;
	points: SeriesPoint[];
	/** Line colour, e.g. a service hue (serviceSeriesColor). */
	color?: string;
}

export interface LineChart extends Mounted {
	update(series: Series[]): void;
	resize(): void;
}

function lineOption(series: Series[], unit: string) {
	return {
		animation: false,
		tooltip: {
			trigger: 'axis',
			valueFormatter: (v: unknown) => (v === null || v === undefined ? '—' : `${v}${unit}`)
		},
		xAxis: { type: 'time' },
		yAxis: { type: 'value', axisLabel: { formatter: `{value}${unit}` } },
		series: series.map((s) => ({
			type: 'line',
			name: s.name,
			showSymbol: false,
			connectNulls: false,
			lineStyle: s.color ? { color: s.color, width: 1.75 } : { width: 1.75 },
			itemStyle: s.color ? { color: s.color } : undefined,
			data: s.points.map((p) => [p.at.getTime(), p.value])
		}))
	};
}

/**
 * Apache ECharts line chart, tree-shaken to the parts in ./echarts.ts (host
 * and container metrics, #5), themed with the tokens. `unit` suffixes axis
 * and tooltip values ("%", " MB").
 */
export async function mountLineChart(
	el: HTMLElement,
	name: string,
	points: SeriesPoint[],
	unit = ''
): Promise<LineChart> {
	const { init, DOCKYARD_ECHARTS_THEME } = await import('./echarts');
	const chart = init(el, DOCKYARD_ECHARTS_THEME, { renderer: 'canvas' });
	chart.setOption(lineOption([{ name, points }], unit));
	return {
		destroy: () => chart.dispose(),
		update: (series) => chart.setOption(lineOption(series, unit), { replaceMerge: ['series'] }),
		resize: () => chart.resize()
	};
}

/** One line of a time-series chart; values align with the timestamps. */
export interface TimeSeriesLine {
	name: string;
	/** null is a gap (no sample): a break in the line, never zero (#5). */
	values: (number | null)[];
	color?: string;
	/** A faint fill under the line. */
	area?: boolean;
}

export interface TimeSeriesOptions {
	/** Bucket times, ms since the epoch, ascending. */
	timestamps: number[];
	lines: TimeSeriesLine[];
	/** Formats axis labels and tooltip values ("12.4%", "1.8 GB"). */
	format: (v: number) => string;
	/** The x axis spans exactly this range (so trailing gaps show). */
	from: number;
	to: number;
	yMin?: number;
	yMax?: number;
	/** Ranges without samples, shaded (offline intervals). */
	gaps?: { from: number; to: number }[];
}

export interface TimeSeriesChart extends Mounted {
	update(opts: TimeSeriesOptions): void;
	resize(): void;
}

export function timeSeriesOption(o: TimeSeriesOptions) {
	const gapArea = o.gaps?.length
		? {
				silent: true,
				itemStyle: { color: CHART_COLORS.gap },
				data: o.gaps.map((g) => [{ xAxis: g.from }, { xAxis: g.to }])
			}
		: undefined;
	return {
		animation: false,
		grid: { left: 4, right: 12, top: 12, bottom: 4, containLabel: true },
		tooltip: {
			trigger: 'axis',
			valueFormatter: (v: unknown) => (typeof v === 'number' ? o.format(v) : 'No sample')
		},
		xAxis: { type: 'time', min: o.from, max: o.to },
		yAxis: {
			type: 'value',
			min: o.yMin,
			max: o.yMax,
			// A known maximum (100 %, total memory) gets four even steps.
			interval: o.yMax ? o.yMax / 4 : undefined,
			axisLabel: { formatter: (v: number) => o.format(v) }
		},
		series: o.lines.map((l, i) => ({
			type: 'line',
			name: l.name,
			showSymbol: false,
			connectNulls: false,
			lineStyle: l.color ? { color: l.color, width: 1.75 } : { width: 1.75 },
			itemStyle: l.color ? { color: l.color } : undefined,
			areaStyle: l.area ? { color: l.color, opacity: 0.08 } : undefined,
			data: o.timestamps.map((t, j) => [t, l.values[j] ?? null]),
			markArea: i === 0 ? gapArea : undefined
		}))
	};
}

/**
 * A time-series line chart with an exact time range, formatted values and
 * shaded gaps (host metrics, #5). Lines never connect across nulls.
 */
export async function mountTimeSeries(
	el: HTMLElement,
	opts: TimeSeriesOptions
): Promise<TimeSeriesChart> {
	const { init, DOCKYARD_ECHARTS_THEME } = await import('./echarts');
	const chart = init(el, DOCKYARD_ECHARTS_THEME, { renderer: 'canvas' });
	chart.setOption(timeSeriesOption(opts));
	return {
		destroy: () => chart.dispose(),
		update: (o) => chart.setOption(timeSeriesOption(o), { replaceMerge: ['series'] }),
		resize: () => chart.resize()
	};
}

export interface Sparkline extends Mounted {
	update(values: (number | null)[]): void;
}

/** A tiny axis-less line (KPI cards, #22), gaps as breaks. */
export async function mountSparkline(
	el: HTMLElement,
	values: (number | null)[],
	color: string = CHART_COLORS.series[0]
): Promise<Sparkline> {
	const { init, DOCKYARD_ECHARTS_THEME } = await import('./echarts');
	const chart = init(el, DOCKYARD_ECHARTS_THEME, { renderer: 'canvas' });
	const option = (vs: (number | null)[]) => ({
		animation: false,
		grid: { left: 0, right: 0, top: 2, bottom: 2, containLabel: false },
		xAxis: { type: 'category', show: false, boundaryGap: false, data: vs.map((_, i) => i) },
		yAxis: { type: 'value', show: false, scale: true },
		tooltip: { show: false },
		series: [
			{
				type: 'line',
				data: vs,
				showSymbol: false,
				connectNulls: false,
				lineStyle: { color, width: 1.75 },
				areaStyle: { color, opacity: 0.08 }
			}
		]
	});
	chart.setOption(option(values));
	return {
		destroy: () => chart.dispose(),
		update: (vs) => chart.setOption(option(vs))
	};
}

export interface TerminalHandle extends Mounted {
	write(data: string): void;
	/** Keystrokes typed by the user (for an exec session, #8). */
	onData(cb: (data: string) => void): void;
	fit(): { cols: number; rows: number };
	focus(): void;
}

export interface TerminalOptions {
	/** Read-only output (e.g. build logs) when true. */
	readOnly?: boolean;
	rows?: number;
}

/** xterm.js terminal (container exec, #8), themed with the tokens. */
export async function mountTerminal(
	el: HTMLElement,
	opts: TerminalOptions = {}
): Promise<TerminalHandle> {
	const [{ Terminal }] = await Promise.all([
		import('@xterm/xterm'),
		import('@xterm/xterm/css/xterm.css')
	]);
	const term = new Terminal({
		convertEol: true,
		disableStdin: !!opts.readOnly,
		rows: opts.rows ?? 8,
		fontFamily: EDITOR_COLORS.fontMono,
		fontSize: 13,
		lineHeight: 1.25,
		cursorBlink: !opts.readOnly,
		theme: { ...TERMINAL_THEME }
	});
	term.open(el);
	return {
		destroy: () => term.dispose(),
		write: (d) => term.write(d),
		onData: (cb) => void term.onData(cb),
		fit: () => ({ cols: term.cols, rows: term.rows }),
		focus: () => term.focus()
	};
}
