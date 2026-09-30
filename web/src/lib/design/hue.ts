// Tile colours (#22). Colours belong to types, never to single items:
// every service has the same tile and the same colour, also where several
// services' output is interleaved (a stack's merged logs, their chips and
// chart series), which tell services apart by name, never by colour. The
// one exception is a chart drawing every container of an environment
// (rankColor): there a colour per line is the only way to follow one.
//
// Pure functions; no Svelte. Components: IconTile, the log viewer, charts.

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

/** The one colour of every service (the service entry of RESOURCE_ICONS uses it too). */
export const SERVICE_COLOR: TileColor = 'blue';

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

/** The colour of service log prefixes, chips and chart series (canvases). */
export const SERVICE_HEX = TILE_HEX[SERVICE_COLOR].fg;

/**
 * The line colour of the item ranked `rank` (0: the largest) of `count` on
 * a chart drawing many items of one type at once (every container of an
 * environment), in Beszel's order: hues spread evenly around the wheel from
 * red, at Beszel's dark-theme saturation and lightness. HSL (ECharts and
 * CSS both read it).
 */
export function rankColor(rank: number, count: number): string {
	const hue = Math.round(((rank * 360) / Math.max(count, 1)) % 360);
	return `hsl(${hue}, 60%, 55%)`;
}
