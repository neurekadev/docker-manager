// Lazy entry points for the heavy UI libraries (#11,
// docs/internal/adr/0002-frontend-libraries.md). Each library is reachable ONLY
// through a dynamic import() in these functions, so it is split into its
// own chunks and fetched when a view first needs it. The build check
// (scripts/verify-build.mjs) fails if any of them lands in a statically
// imported chunk. Views import these helpers (or the $lib/ui wrappers
// CodeEditor, Sparkline, TerminalView), never the libraries. Every mount
// applies Docker Manager's theme (#22): ./codemirror-theme.ts, ./echarts-theme.ts,
// TERMINAL_THEME in ./palette.ts.
import type { StreamParser } from '@codemirror/language';
import type { Extension } from '@codemirror/state';
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
	return mountCodeEditor(parent, doc, { ...opts, language: 'yaml' });
}

/**
 * Languages of the file editor (#15). Everything but YAML and JSON uses a
 * CodeMirror legacy stream mode; `markdown` and `text` are plain text.
 */
export const EDITOR_LANGUAGES = [
	'yaml',
	'json',
	'shell',
	'dockerfile',
	'nginx',
	'properties',
	'toml',
	'xml',
	'markdown',
	'text'
] as const;
export type EditorLanguage = (typeof EDITOR_LANGUAGES)[number];

export interface CodeEditorOptions extends EditorOptions {
	language?: EditorLanguage;
	/** Wrap long lines instead of scrolling sideways. */
	wrap?: boolean;
}

export interface CodeEditorHandle extends YamlEditor {
	/** Switches the syntax highlighting (keeps text and undo history). */
	setLanguage(language: EditorLanguage): Promise<void>;
	setReadOnly(readOnly: boolean): void;
	/** Opens CodeMirror's search and replace panel. */
	openSearch(): void;
	/** Wraps long lines (true) or scrolls sideways (false). */
	setWrap?(wrap: boolean): void;
}

async function languageSupport(language: EditorLanguage): Promise<Extension> {
	const legacy = async (load: () => Promise<StreamParser<unknown>>) => {
		const [{ StreamLanguage }, parser] = await Promise.all([
			import('@codemirror/language'),
			load()
		]);
		return StreamLanguage.define(parser);
	};
	switch (language) {
		case 'yaml':
			return (await import('@codemirror/lang-yaml')).yaml();
		case 'json':
			return (await import('@codemirror/lang-json')).json();
		case 'shell':
			return legacy(async () => (await import('@codemirror/legacy-modes/mode/shell')).shell);
		case 'dockerfile':
			return legacy(
				async () => (await import('@codemirror/legacy-modes/mode/dockerfile')).dockerFile
			);
		case 'nginx':
			return legacy(async () => (await import('@codemirror/legacy-modes/mode/nginx')).nginx);
		case 'properties':
			return legacy(
				async () => (await import('@codemirror/legacy-modes/mode/properties')).properties
			);
		case 'toml':
			return legacy(async () => (await import('@codemirror/legacy-modes/mode/toml')).toml);
		case 'xml':
			return legacy(async () => (await import('@codemirror/legacy-modes/mode/xml')).xml);
		default:
			return [];
	}
}

/**
 * CodeMirror 6 for the file editor (#15): line numbers, undo/redo, search
 * and replace (Mod-f, or openSearch()), bracket matching, the language's
 * highlighting, Docker Manager's theme. Tab keeps moving focus (no tab trap).
 */
export async function mountCodeEditor(
	parent: HTMLElement,
	doc: string,
	opts: CodeEditorOptions = {}
): Promise<CodeEditorHandle> {
	const [{ EditorView, basicSetup }, { EditorState, Compartment }, search, theme, lang] =
		await Promise.all([
			import('codemirror'),
			import('@codemirror/state'),
			import('@codemirror/search'),
			import('./codemirror-theme'),
			languageSupport(opts.language ?? 'text')
		]);
	const language = new Compartment();
	const readOnly = new Compartment();
	const wrapping = new Compartment();
	const extensions = [
		basicSetup,
		language.of(lang),
		theme.dockerManagerEditorTheme,
		readOnly.of(EditorState.readOnly.of(!!opts.readOnly)),
		wrapping.of(opts.wrap ? EditorView.lineWrapping : []),
		EditorView.contentAttributes.of({ 'aria-label': opts.label ?? 'Editor' }),
		EditorView.updateListener.of((u) => {
			if (u.docChanged) opts.onChange?.(u.state.doc.toString());
		})
	];
	const view = new EditorView({ doc, extensions, parent });
	let version = 0;
	return {
		destroy: () => view.destroy(),
		text: () => view.state.doc.toString(),
		setText: (text) =>
			view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }),
		focus: () => view.focus(),
		setLanguage: async (l) => {
			const mine = ++version;
			const ext = await languageSupport(l);
			if (mine === version) view.dispatch({ effects: language.reconfigure(ext) });
		},
		setReadOnly: (ro) =>
			view.dispatch({ effects: readOnly.reconfigure(EditorState.readOnly.of(ro)) }),
		openSearch: () => {
			search.openSearchPanel(view);
		},
		setWrap: (on) =>
			view.dispatch({ effects: wrapping.reconfigure(on ? EditorView.lineWrapping : []) })
	};
}

function parseJson(text: string): unknown {
	try {
		return JSON.parse(text) as unknown;
	} catch (e) {
		throw new Error(`This isn't valid JSON: ${e instanceof Error ? e.message : e}`, {
			cause: e
		});
	}
}

/**
 * Reformats YAML (comments kept, 2-space indentation) or JSON (2 spaces)
 * for the editor's Format button and its Beautify entry. Throws an Error
 * naming the line when the text does not parse.
 */
export async function formatDocument(text: string, language: 'yaml' | 'json'): Promise<string> {
	if (language === 'json') return JSON.stringify(parseJson(text), null, 2) + '\n';
	const { parseAllDocuments } = await import('yaml');
	const docs = parseAllDocuments(text, { prettyErrors: true });
	const list = Array.isArray(docs) ? docs : [docs];
	for (const d of list) {
		const err = d.errors[0];
		if (err) {
			const line = err.linePos?.[0]?.line;
			throw new Error(
				`This isn't valid YAML${line ? ` (line ${line})` : ''}: ${err.message.split('\n')[0]}`
			);
		}
	}
	return list.map((d) => d.toString({ indent: 2, lineWidth: 0 })).join('');
}

/**
 * Compacts JSON to one line (no spaces, a final newline) for the editor's
 * Minify entry. Only JSON: whitespace carries no meaning there, while YAML
 * depends on its indentation. Throws like formatDocument when the text
 * does not parse (the same "This isn't valid JSON: …" message).
 */
export function minifyJson(text: string): string {
	return JSON.stringify(parseJson(text)) + '\n';
}

/**
 * Reads a YAML document into plain values for read-only summaries (a
 * template's services, images and ports). Throws when it does not parse.
 */
export async function parseYaml(text: string): Promise<unknown> {
	const { parse } = await import('yaml');
	return parse(text) as unknown;
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
	const { init, DOCKER_MANAGER_ECHARTS_THEME } = await import('./echarts');
	const chart = init(el, DOCKER_MANAGER_ECHARTS_THEME, { renderer: 'canvas' });
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
	const { init, DOCKER_MANAGER_ECHARTS_THEME } = await import('./echarts');
	const chart = init(el, DOCKER_MANAGER_ECHARTS_THEME, { renderer: 'canvas' });
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
	const { init, DOCKER_MANAGER_ECHARTS_THEME } = await import('./echarts');
	const chart = init(el, DOCKER_MANAGER_ECHARTS_THEME, { renderer: 'canvas' });
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
	// KPI cards change width with their grid (container queries, the
	// sidebar rail); follow the element instead of keeping the first size.
	const observer =
		typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => chart.resize());
	observer?.observe(el);
	return {
		destroy: () => {
			observer?.disconnect();
			chart.dispose();
		},
		update: (vs) => chart.setOption(option(vs))
	};
}

export interface TerminalHandle extends Mounted {
	write(data: string | Uint8Array): void;
	/** Keystrokes typed by the user (for an exec session, #8). */
	onData(cb: (data: string) => void): void;
	/** Size changes of the terminal grid (after fit()). */
	onResize(cb: (size: { cols: number; rows: number }) => void): void;
	/**
	 * With `fit` (TerminalOptions) resizes the grid to the element and
	 * returns the new size; otherwise returns the current size.
	 */
	fit(): { cols: number; rows: number };
	clear(): void;
	/** Turns keyboard input on or off (a closed session is read-only). */
	setInputEnabled(enabled: boolean): void;
	focus(): void;
}

export interface TerminalOptions {
	/** Read-only output (e.g. build logs) when true. */
	readOnly?: boolean;
	rows?: number;
	/** Size the grid to the element (@xterm/addon-fit): full-height terminals. */
	fit?: boolean;
	/**
	 * Keep "\n" as a line feed only (a TTY's output already carries "\r\n").
	 * Default false: "\n" also returns the carriage (plain output, logs).
	 */
	rawNewlines?: boolean;
}

/** xterm.js terminal (container exec, #8), themed with the tokens. */
export async function mountTerminal(
	el: HTMLElement,
	opts: TerminalOptions = {}
): Promise<TerminalHandle> {
	const [{ Terminal }, fitAddon] = await Promise.all([
		import('@xterm/xterm'),
		opts.fit ? import('@xterm/addon-fit') : Promise.resolve(null),
		import('@xterm/xterm/css/xterm.css')
	]);
	const term = new Terminal({
		convertEol: !opts.rawNewlines,
		disableStdin: !!opts.readOnly,
		rows: opts.rows ?? 8,
		fontFamily: EDITOR_COLORS.fontMono,
		fontSize: 13,
		lineHeight: 1.25,
		cursorBlink: !opts.readOnly,
		theme: { ...TERMINAL_THEME }
	});
	const fitter = fitAddon ? new fitAddon.FitAddon() : null;
	if (fitter) term.loadAddon(fitter);
	term.open(el);
	return {
		destroy: () => term.dispose(),
		write: (d) => term.write(d),
		onData: (cb) => void term.onData(cb),
		onResize: (cb) => void term.onResize(cb),
		fit: () => {
			try {
				fitter?.fit();
			} catch {
				// Not laid out yet (hidden element): keep the current size.
			}
			return { cols: term.cols, rows: term.rows };
		},
		clear: () => term.clear(),
		setInputEnabled: (on) => {
			term.options.disableStdin = !on;
			term.options.cursorBlink = on;
		},
		focus: () => term.focus()
	};
}
