// Service hue identity (#22 design brief, "the one memorable thing"): every
// service gets a stable tile colour derived from stackId + serviceName, so
// "silo-db" has the same colour in the services table, the log viewer's
// name prefix, its chart series and its filter chip. An explicit icon in
// the service's display metadata keeps its category colour instead.
//
// Pure functions; no Svelte. Components: IconTile, ServiceChip (feature
// views), chart series (serviceSeriesColor).

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

/**
 * Service icon names (Lucide, kebab-case as stored in display metadata)
 * with their category colour. Unknown names fall back to "box".
 */
export const SERVICE_ICON_CATEGORY: Record<string, TileColor> = {
	globe: 'blue',
	box: 'blue',
	server: 'blue',
	'app-window': 'blue',
	database: 'teal',
	'hard-drive': 'teal',
	layers: 'rose',
	zap: 'rose',
	cog: 'slate',
	workflow: 'slate',
	clapperboard: 'violet',
	film: 'violet',
	shield: 'green',
	'message-square': 'indigo',
	cpu: 'cyan'
};

export interface ServiceIdentity {
	/** Lucide icon name (a key of SERVICE_ICON_CATEGORY). */
	icon: string;
	color: TileColor;
}

const IMAGE_RULES: [RegExp, string][] = [
	[
		/(^|[/-])(postgres|postgis|mysql|mariadb|mongo|cockroach|clickhouse|influxdb|timescale|couchdb)/,
		'database'
	],
	[/(^|[/-])(redis|valkey|memcached|keydb|dragonfly)/, 'layers'],
	[/(^|[/-])(nginx|caddy|traefik|httpd|haproxy|apache|envoy|web)([:/-]|$)/, 'globe'],
	[/(worker|celery|sidekiq|queue|cron)/, 'cog'],
	[/(jellyfin|plex|emby)/, 'clapperboard'],
	[/(rabbitmq|nats|mosquitto|kafka)/, 'message-square']
];

/** Lucide icon for an image reference or service name (#22 heuristic). */
export function iconForImage(image: string, serviceName = ''): string {
	const probe = `${image.toLowerCase()} ${serviceName.toLowerCase()}`;
	for (const [re, icon] of IMAGE_RULES) if (re.test(probe)) return icon;
	return 'box';
}

/**
 * A service's icon and tile colour. With an explicit icon override the
 * icon keeps its category colour; otherwise the icon comes from the image
 * heuristic and the colour from the stable hue.
 */
export function serviceIdentity(opts: {
	stackId: string;
	name: string;
	image?: string;
	icon?: string | null;
}): ServiceIdentity {
	const override = opts.icon?.trim();
	if (override) {
		const icon = override in SERVICE_ICON_CATEGORY ? override : 'box';
		return { icon, color: SERVICE_ICON_CATEGORY[icon] };
	}
	return {
		icon: iconForImage(opts.image ?? '', opts.name),
		color: serviceHue(opts.stackId, opts.name)
	};
}
