// Token values for canvases and third-party themes that cannot read CSS
// custom properties (CodeMirror's theme object, ECharts, xterm.js). Keep
// equal to src/lib/design/tokens.css (checked by palette.spec.ts).

export const EDITOR_COLORS = {
	background: '#0e141b', // --code-bg
	text: '#c8d3e2', // --text-default
	muted: '#8392a8', // --text-muted
	caret: '#52a3f7', // --accent-text
	selection: '#1d3561', // --code-selection
	activeLine: '#131b25', // --code-active-line
	gutter: '#596476', // --code-gutter
	gutterActive: '#c8d3e2', // --code-gutter-active
	key: '#52a3f7', // --code-key
	string: '#e8c07d', // --code-string
	number: '#f59f6b', // --code-number
	url: '#fd6b66', // --code-url
	comment: '#596476', // --code-comment
	punctuation: '#8392a8', // --code-punctuation
	keyword: '#707ffc', // --tile-violet-fg
	surfaceRaised: '#151b24', // --surface-raised
	border: '#263041', // --border-strong
	fontMono: "'JetBrains Mono Variable', 'JetBrains Mono', ui-monospace, Consolas, monospace",
	fontSans: "'Inter Variable', Inter, ui-sans-serif, system-ui, sans-serif"
} as const;

export const CHART_COLORS = {
	text: '#8392a8', // --text-muted
	textStrong: '#f2f4f7', // --text-strong
	grid: '#1c2430', // --border-subtle
	axis: '#263041', // --border-strong
	tooltipBg: '#151b24', // --surface-raised
	/** Default series order: CPU (info), memory (indigo), then the tile hues. */
	series: [
		'#2bb0f6',
		'#3199ff',
		'#4ff98b',
		'#707ffc',
		'#46ffce',
		'#fc737d',
		'#b4c4f2',
		'#5b92fd'
	],
	ok: '#4cf683',
	warn: '#f5b544',
	danger: '#fd6b66'
} as const;

export const TERMINAL_THEME = {
	background: '#0e141b',
	foreground: '#c8d3e2',
	cursor: '#52a3f7',
	cursorAccent: '#0e141b',
	selectionBackground: '#1d3561',
	black: '#10161d',
	red: '#fd6b66',
	green: '#4cf683',
	yellow: '#f5b544',
	blue: '#5b92fd',
	magenta: '#707ffc',
	cyan: '#2bb0f6',
	white: '#c8d3e2',
	brightBlack: '#596476',
	brightRed: '#fc737d',
	brightGreen: '#4ff98b',
	brightYellow: '#e8c07d',
	brightBlue: '#52a3f7',
	brightMagenta: '#b4c4f2',
	brightCyan: '#46ffce',
	brightWhite: '#f2f4f7'
} as const;
