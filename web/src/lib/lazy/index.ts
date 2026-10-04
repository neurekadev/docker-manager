// Lazy entry points for the heavy UI libraries (#11,
// docs/internal/adr/0002-frontend-libraries.md). Each library is reachable ONLY
// through a dynamic import() in these functions, so it is split into its
// own chunks and fetched when a view first needs it. The build check
// (scripts/verify-build.mjs) fails if any of them lands in a statically
// imported chunk. Views import these helpers (or the $lib/ui wrappers
// CodeEditor, Sparkline, TerminalView), never the libraries. Every mount
// applies Docker Manager's theme (#22): ./codemirror-theme.ts, ./echarts-theme.ts,
// TERMINAL_THEME in ./palette.ts.
import type { LanguageDescription, LanguageSupport, StreamParser } from '@codemirror/language';
import type { Token } from 'marked';
import { formatNumber } from '$lib/ui/format';
import { languageByName, type EditorLanguage } from './languages';
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

export { EDITOR_LANGUAGES, languageByName, type EditorLanguage } from './languages';

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

type Legacy = () => Promise<StreamParser<unknown>>;

/** The legacy stream modes, each loaded on first use. */
const LEGACY: Partial<Record<EditorLanguage, Legacy>> = {
	shell: async () => (await import('@codemirror/legacy-modes/mode/shell')).shell,
	dockerfile: async () => (await import('@codemirror/legacy-modes/mode/dockerfile')).dockerFile,
	nginx: async () => (await import('@codemirror/legacy-modes/mode/nginx')).nginx,
	properties: async () => (await import('@codemirror/legacy-modes/mode/properties')).properties,
	toml: async () => (await import('@codemirror/legacy-modes/mode/toml')).toml,
	xml: async () => (await import('@codemirror/legacy-modes/mode/xml')).xml,
	scss: async () => (await import('@codemirror/legacy-modes/mode/css')).sCSS,
	less: async () => (await import('@codemirror/legacy-modes/mode/css')).less,
	python: async () => (await import('@codemirror/legacy-modes/mode/python')).python,
	go: async () => (await import('@codemirror/legacy-modes/mode/go')).go,
	rust: async () => (await import('@codemirror/legacy-modes/mode/rust')).rust,
	ruby: async () => (await import('@codemirror/legacy-modes/mode/ruby')).ruby,
	lua: async () => (await import('@codemirror/legacy-modes/mode/lua')).lua,
	perl: async () => (await import('@codemirror/legacy-modes/mode/perl')).perl,
	powershell: async () => (await import('@codemirror/legacy-modes/mode/powershell')).powerShell,
	sql: async () => (await import('@codemirror/legacy-modes/mode/sql')).standardSQL,
	diff: async () => (await import('@codemirror/legacy-modes/mode/diff')).diff,
	c: async () => (await import('@codemirror/legacy-modes/mode/clike')).c,
	cpp: async () => (await import('@codemirror/legacy-modes/mode/clike')).cpp,
	csharp: async () => (await import('@codemirror/legacy-modes/mode/clike')).csharp,
	java: async () => (await import('@codemirror/legacy-modes/mode/clike')).java,
	kotlin: async () => (await import('@codemirror/legacy-modes/mode/clike')).kotlin,
	groovy: async () => (await import('@codemirror/legacy-modes/mode/groovy')).groovy,
	swift: async () => (await import('@codemirror/legacy-modes/mode/swift')).swift,
	protobuf: async () => (await import('@codemirror/legacy-modes/mode/protobuf')).protobuf,
	cmake: async () => (await import('@codemirror/legacy-modes/mode/cmake')).cmake,
	jinja2: async () => (await import('@codemirror/legacy-modes/mode/jinja2')).jinja2
};

/** The language's CodeMirror support, or null for plain text. */
async function languageSupport(language: EditorLanguage): Promise<LanguageSupport | null> {
	switch (language) {
		case 'yaml':
			return (await import('@codemirror/lang-yaml')).yaml();
		case 'json':
			return (await import('@codemirror/lang-json')).json();
		case 'html':
			return (await import('@codemirror/lang-html')).html();
		case 'css':
			return (await import('@codemirror/lang-css')).css();
		case 'javascript':
			return (await import('@codemirror/lang-javascript')).javascript({ jsx: true });
		case 'typescript':
			return (await import('@codemirror/lang-javascript')).javascript({
				jsx: true,
				typescript: true
			});
		case 'markdown': {
			const [{ markdown, markdownLanguage }, { LanguageDescription: Description }] =
				await Promise.all([
					import('@codemirror/lang-markdown'),
					import('@codemirror/language')
				]);
			// Fenced code is highlighted in the language its fence names. One
			// description per language: lang-markdown uses its parser only once
			// the description has loaded, so a new one per parse never would.
			const fenced = new Map<EditorLanguage, LanguageDescription>();
			return markdown({
				base: markdownLanguage,
				codeLanguages: (info) => {
					const l = languageByName(info);
					if (!l || l === 'text' || l === 'markdown') return null;
					let d = fenced.get(l);
					if (!d) {
						d = Description.of({
							name: l,
							load: async () => {
								const support = await languageSupport(l);
								if (!support) throw new Error(`No highlighting for ${l}`);
								return support;
							}
						});
						fenced.set(l, d);
					}
					return d;
				}
			});
		}
	}
	const load = LEGACY[language];
	if (!load) return null;
	const [{ StreamLanguage, LanguageSupport: Support }, parser] = await Promise.all([
		import('@codemirror/language'),
		load()
	]);
	return new Support(StreamLanguage.define(parser));
}

/**
 * CodeMirror 6 for the file editor (#15): line numbers, undo/redo, search
 * and replace (Mod-f, or openSearch()), bracket matching, the language's
 * highlighting, Docker Manager's theme. Tab and Shift-Tab indent and
 * outdent (indentWithTab); the editor is still no tab trap: Escape, then
 * Tab or Shift-Tab within two seconds, moves focus out (CodeMirror's tab
 * focus mode, also toggled with Ctrl-m / Shift-Alt-m on macOS).
 */
export async function mountCodeEditor(
	parent: HTMLElement,
	doc: string,
	opts: CodeEditorOptions = {}
): Promise<CodeEditorHandle> {
	const [
		{ EditorView, basicSetup },
		{ EditorState, Compartment },
		{ keymap },
		{ indentWithTab },
		search,
		theme,
		lang
	] = await Promise.all([
		import('codemirror'),
		import('@codemirror/state'),
		import('@codemirror/view'),
		import('@codemirror/commands'),
		import('@codemirror/search'),
		import('./codemirror-theme'),
		languageSupport(opts.language ?? 'text')
	]);
	const language = new Compartment();
	const readOnly = new Compartment();
	const wrapping = new Compartment();
	const extensions = [
		basicSetup,
		keymap.of([indentWithTab]),
		language.of(lang ?? []),
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
			if (mine === version) view.dispatch({ effects: language.reconfigure(ext ?? []) });
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

/** A run of highlighted code: its text and highlight classes ("tok-keyword"; empty when plain). */
export interface CodeSpan {
	text: string;
	classes: string;
}

/**
 * Highlights code for a static view (the Markdown preview's fenced code)
 * with the editor's parser for `language`: one list of spans per line,
 * classed by @lezer/highlight's classHighlighter (tok-keyword, tok-string,
 * …) for the view to colour. Plain spans when the language has no parser.
 */
export async function highlightCode(code: string, language: EditorLanguage): Promise<CodeSpan[][]> {
	const support = await languageSupport(language);
	if (!support) return code.split('\n').map((text) => [{ text, classes: '' }]);
	const { highlightCode: highlight, classHighlighter } = await import('@lezer/highlight');
	const lines: CodeSpan[][] = [[]];
	highlight(
		code,
		support.language.parser.parse(code),
		classHighlighter,
		(text, classes) => lines[lines.length - 1].push({ text, classes }),
		() => lines.push([])
	);
	return lines;
}

/**
 * Reads GitHub Flavored Markdown into marked's tokens (tables, task lists,
 * strikethrough, autolinks). The caller renders them as elements, never as
 * HTML ($lib/features/files/markdown.ts).
 */
export async function lexMarkdown(src: string): Promise<Token[]> {
	const { Lexer } = await import('marked');
	return Lexer.lex(src, { gfm: true });
}

export interface SeriesPoint {
	at: Date;
	/** null is a gap (no sample), rendered as a break, never as zero (#5). */
	value: number | null;
}

export interface Series {
	name: string;
	points: SeriesPoint[];
	/** Line colour, e.g. SERVICE_HEX or a TILE_HEX colour. */
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
			valueFormatter: (v: unknown) =>
				typeof v === 'number' ? `${formatNumber(v)}${unit}` : '—'
		},
		xAxis: { type: 'time' },
		yAxis: {
			type: 'value',
			axisLabel: { formatter: (v: number) => `${formatNumber(v)}${unit}` }
		},
		series: series.map((s) => ({
			type: 'line',
			name: s.name,
			showSymbol: false,
			connectNulls: false,
			smooth: true,
			smoothMonotone: 'x',
			lineStyle: s.color ? { color: s.color, width: 1.5 } : { width: 1.5 },
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
	/** A filled area under the line (a 1 px line over the fill). */
	area?: boolean;
	/** Opacity of the area's fill (default AREA_FILL; Beszel's network 0.2). */
	fill?: number;
	/** A dashed line (a reference next to a solid one; identity not by color alone). */
	dashed?: boolean;
	/** Greyed out behind the others (left out by a filter). */
	muted?: boolean;
}

/** Beszel's fill opacity of an area. */
export const AREA_FILL = 0.4;

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
	/**
	 * Stack the lines as filled areas (parts of a whole: memory, every
	 * container). Lines stack in the given order, the first at the bottom.
	 * Every chart is drawn like Beszel's: monotone curves, areas as 1 px
	 * lines over 40 % fills, plain lines 1.5 px, a dot on each shown line
	 * where the pointer is.
	 */
	stacked?: boolean;
	/**
	 * The tooltip of a bucket as HTML (the caller escapes names), replacing
	 * ECharts' own list; it may be long and is placed beside the pointer,
	 * inside the window.
	 */
	tooltip?: (index: number) => string;
	/**
	 * No floating tooltip, only the pointer line (phones: the caller shows
	 * the details in the page, see onPointer).
	 */
	hideTooltip?: boolean;
	/**
	 * Called with the time under the pointer (a hover, a tap) as the axis
	 * pointer moves; the caller maps it to a bucket.
	 */
	onPointer?: (time: number) => void;
}

/** A tooltip placement: the pointer and tooltip size in chart coordinates. */
export type TooltipPlace = (point: number[], size: { contentSize: number[] }) => number[];

/**
 * Where a tooltip of `size` goes next to the pointer at `point` (chart
 * coordinates) so it stays inside the window: right of the pointer, else
 * left of it, vertically centred on it and moved up or down to fit (a
 * tooltip taller than the window starts at its top). `chart` is the chart's
 * position in the window.
 */
export function besidePointer(
	point: readonly number[],
	size: readonly number[],
	chart: { left: number; top: number },
	view: { width: number; height: number },
	margin = 8,
	gap = 16
): [number, number] {
	const [px, py] = point;
	const [w, h] = size;
	let x = px + gap;
	if (chart.left + x + w > view.width - margin) x = px - gap - w;
	x = Math.max(x, margin - chart.left);
	let y = py - h / 2;
	y = Math.min(y, view.height - margin - h - chart.top);
	y = Math.max(y, margin - chart.top);
	return [x, y];
}

export interface TimeSeriesChart extends Mounted {
	update(opts: TimeSeriesOptions): void;
	resize(): void;
}

export function timeSeriesOption(o: TimeSeriesOptions, place?: TooltipPlace) {
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
			valueFormatter: (v: unknown) => (typeof v === 'number' ? o.format(v) : 'No sample'),
			...(o.hideTooltip
				? { showContent: false, triggerOn: 'mousemove|click' }
				: o.tooltip
					? customTooltip(o.tooltip, place)
					: {})
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
		series: o.lines.map((l, i) => {
			const color = l.muted ? CHART_COLORS.text : l.color;
			const area = l.area || o.stacked;
			return {
				type: 'line',
				name: l.name,
				showSymbol: false,
				connectNulls: false,
				...(o.stacked ? { stack: 'total' } : {}),
				smooth: true,
				smoothMonotone: 'x',
				// The hover dot of a shown line (muted lines have none).
				symbol: l.muted ? 'none' : 'circle',
				symbolSize: 6,
				// Muted lines stay behind the others.
				z: l.muted ? 1 : 2,
				lineStyle: {
					...(color ? { color } : {}),
					width: area ? 1 : 1.5,
					...(l.dashed ? { type: 'dashed' } : {}),
					...(l.muted ? { opacity: 0.35 } : {})
				},
				itemStyle: color ? { color } : undefined,
				areaStyle: area
					? { color, opacity: l.muted ? 0.04 : (l.fill ?? AREA_FILL) }
					: undefined,
				data: o.timestamps.map((t, j) => [t, l.values[j] ?? null]),
				markArea: i === 0 ? gapArea : undefined
			};
		})
	};
}

/** The tooltip options of TimeSeriesOptions.tooltip (one bucket's HTML). */
function customTooltip(html: (index: number) => string, place?: TooltipPlace) {
	return {
		formatter: (params: unknown) => {
			const first = (Array.isArray(params) ? params[0] : params) as
				{ dataIndex?: number } | undefined;
			return first?.dataIndex === undefined ? '' : html(first.dataIndex);
		},
		// Outside the card, so a long list is not clipped by the chart.
		appendTo: 'body',
		...(place
			? {
					position: (
						point: number[],
						_params: unknown,
						_dom: unknown,
						_rect: unknown,
						size: { contentSize: number[] }
					) => place(point, size)
				}
			: {})
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
	const place: TooltipPlace = (point, size) =>
		besidePointer(point, size.contentSize, el.getBoundingClientRect(), {
			width: window.innerWidth,
			height: window.innerHeight
		});
	let current = opts;
	chart.setOption(timeSeriesOption(opts, place));
	// The axis pointer follows a hover or a tap, also without a tooltip.
	chart.on('updateAxisPointer', (e: unknown) => {
		const value = (e as { axesInfo?: { value?: unknown }[] }).axesInfo?.[0]?.value;
		if (typeof value === 'number') current.onPointer?.(value);
	});
	return {
		destroy: () => chart.dispose(),
		update: (o) => {
			current = o;
			chart.setOption(timeSeriesOption(o, place), { replaceMerge: ['series'] });
		},
		resize: () => chart.resize()
	};
}

export interface Sparkline extends Mounted {
	update(values: (number | null)[]): void;
}

/** A tiny axis-less area (KPI cards, #22) drawn like the charts, gaps as breaks. */
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
				smooth: true,
				smoothMonotone: 'x',
				lineStyle: { color, width: 1 },
				areaStyle: { color, opacity: AREA_FILL }
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
