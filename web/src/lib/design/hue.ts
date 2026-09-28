// Tile colours and service hues (#22). Services share one tile (the
// service entry of RESOURCE_ICONS); where several services' output is
// interleaved (a stack's merged logs, metrics chart series) each service
// gets a stable colour derived from stackId + serviceName, so its lines and
// series can be told apart.
//
// Pure functions; no Svelte. Components: IconTile, the log viewer, chart
// series (serviceSeriesColor).

/** The eight category tile colours of the mockup (tokens --tile-<c>-bg/fg). */
export const TILE_COLORS = [
	'blue',
	'cyan',
	'indigo',
	'green',
	'violet',
	'teal',
	'rose',
	'slate'
] as const;
export type TileColor = (typeof TILE_COLORS)[number];

/** 32-bit FNV-1a: small, fast, stable across sessions and browsers. */
export function fnv1a(s: string): number {
	let h = 0x811c9dc5;
	for (let i = 0; i < s.length; i++) {
		h ^= s.charCodeAt(i);
		h = Math.imul(h, 0x01000193);
	}
	return h >>> 0;
}

/** The stable hue of a service: the same inputs always give the same colour. */
export function serviceHue(stackId: string, serviceName: string): TileColor {
	return TILE_COLORS[fnv1a(`${stackId}/${serviceName}`) % TILE_COLORS.length];
}

/** CSS custom properties of a tile colour, for style attributes. */
export function tileStyle(color: TileColor): string {
	return `--tile-bg: var(--tile-${color}-bg); --tile-fg: var(--tile-${color}-fg);`;
}

/**
 * Resolved hex values for canvases (ECharts, xterm) that cannot read CSS
 * variables; keep equal to tokens.css (checked by hue.spec.ts).
 */
export const TILE_HEX: Record<TileColor, { bg: string; fg: string }> = {
	blue: { bg: '#1d2d57', fg: '#5b92fd' },
	cyan: { bg: '#172b39', fg: '#2bb0f6' },
	indigo: { bg: '#172235', fg: '#3199ff' },
	green: { bg: '#19322c', fg: '#4ff98b' },
	violet: { bg: '#242848', fg: '#707ffc' },
	teal: { bg: '#162224', fg: '#46ffce' },
	rose: { bg: '#3c0e2a', fg: '#fc737d' },
	slate: { bg: '#23283a', fg: '#b4c4f2' }
};

/** A service's chart series colour (its hue's icon colour). */
export function serviceSeriesColor(stackId: string, serviceName: string): string {
	return TILE_HEX[serviceHue(stackId, serviceName)].fg;
}
