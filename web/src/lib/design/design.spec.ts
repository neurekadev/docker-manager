import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { CHART_COLORS, EDITOR_COLORS, TERMINAL_THEME } from '$lib/lazy/palette';
import { dockerManagerEchartsTheme } from '$lib/lazy/echarts-theme';
import { fnv1a, serviceHue, serviceSeriesColor, TILE_COLORS, TILE_HEX, tileStyle } from './hue';

const tokensCss = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8');

function token(name: string): string {
	const m = tokensCss.match(new RegExp(`--${name}:\\s*([^;]+);`));
	if (!m) throw new Error(`token --${name} missing`);
	return m[1].trim();
}

describe('design tokens (#22 brief)', () => {
	it('match the values sampled from the mockup', () => {
		const brief: Record<string, string> = {
			'surface-canvas': '#0a0f15',
			'surface-shell': '#0d131b',
			'surface-panel': '#121a24',
			'surface-raised': '#19222e',
			'surface-hover': '#1f2935',
			'surface-selected': '#112745',
			'border-subtle': '#1f2a38',
			'border-strong': '#2b3747',
			'text-strong': '#f2f4f7',
			'text-default': '#c8d3e2',
			'text-muted': '#8392a8',
			'text-faint': '#596476',
			accent: '#2566fd',
			'accent-hover': '#3d78ff',
			'accent-text': '#52a3f7',
			'accent-soft': '#112745',
			ok: '#4cf683',
			'ok-soft': '#0f2a1f',
			danger: '#fd6b66',
			'danger-soft': '#3f2029',
			warn: '#f5b544',
			'warn-soft': '#33280f',
			info: '#2bb0f6',
			offline: '#8392a8',
			'radius-sm': '6px',
			'radius-md': '8px',
			'radius-lg': '12px',
			'text-body': '13px',
			'text-title': '28px',
			'text-title-sm': '22px',
			'text-section': '16px',
			'text-subsection': '14px',
			'shadow-float': '0 12px 32px rgb(0 0 0 / 0.45)'
		};
		for (const [k, v] of Object.entries(brief)) expect(token(k), k).toBe(v);
	});

	it('keeps AA contrast for muted text on panels', () => {
		const lum = (hex: string) => {
			const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
			const l = c.map((x) => (x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4));
			return 0.2126 * l[0] + 0.7152 * l[1] + 0.0722 * l[2];
		};
		const ratio = (a: string, b: string) => {
			const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
			return (x + 0.05) / (y + 0.05);
		};
		for (const bg of [
			'surface-panel',
			'surface-raised',
			'surface-shell',
			'surface-canvas',
			'surface-hover',
			'surface-search'
		]) {
			expect(ratio(token('text-muted'), token(bg)), bg).toBeGreaterThanOrEqual(4.5);
			expect(ratio(token('text-default'), token(bg)), bg).toBeGreaterThanOrEqual(4.5);
			expect(ratio(token('accent-text'), token(bg)), bg).toBeGreaterThanOrEqual(4.5);
		}
		expect(ratio(token('danger'), token('danger-soft'))).toBeGreaterThanOrEqual(4.5);
		expect(ratio(token('ok'), token('ok-soft'))).toBeGreaterThanOrEqual(4.5);
		expect(ratio(token('surface-canvas'), token('danger'))).toBeGreaterThanOrEqual(4.5);
		expect(ratio(token('text-on-accent'), token('accent'))).toBeGreaterThanOrEqual(4.5);
	});

	it('mirrors tile colours and canvas palettes exactly', () => {
		for (const c of TILE_COLORS) {
			expect(TILE_HEX[c].bg).toBe(token(`tile-${c}-bg`));
			expect(TILE_HEX[c].fg).toBe(token(`tile-${c}-fg`));
		}
		expect(EDITOR_COLORS.background).toBe(token('code-bg'));
		expect(EDITOR_COLORS.key).toBe(token('code-key'));
		expect(EDITOR_COLORS.string).toBe(token('code-string'));
		expect(EDITOR_COLORS.selection).toBe(token('code-selection'));
		expect(TERMINAL_THEME.background).toBe(token('code-bg'));
		expect(CHART_COLORS.grid).toBe(token('border-subtle'));
		expect(dockerManagerEchartsTheme.color[0]).toBe(token('info'));
		expect(dockerManagerEchartsTheme.line.connectNulls).toBe(false);
	});
});

describe('service hues (logs and chart series)', () => {
	it('is stable and deterministic', () => {
		expect(fnv1a('')).toBe(0x811c9dc5);
		expect(fnv1a('a')).toBe(0xe40c292c);
		const a = serviceHue('stack-1', 'silo-db');
		for (let i = 0; i < 5; i++) expect(serviceHue('stack-1', 'silo-db')).toBe(a);
		expect(TILE_COLORS).toContain(a);
		expect(serviceSeriesColor('stack-1', 'silo-db')).toBe(TILE_HEX[a].fg);
		expect(tileStyle('rose')).toBe(
			'--tile-bg: var(--tile-rose-bg); --tile-fg: var(--tile-rose-fg);'
		);
	});

	it('spreads services over the palette', () => {
		const seen = new Set<string>();
		for (let i = 0; i < 64; i++) seen.add(serviceHue('stack-x', `svc-${i}`));
		expect(seen.size).toBe(TILE_COLORS.length);
		// The stack takes part: the same service name differs across stacks.
		const differs = ['a', 'b', 'c', 'd', 'e', 'f'].some(
			(s) => serviceHue(`stack-${s}`, 'web') !== serviceHue('stack-a', 'web')
		);
		expect(differs).toBe(true);
	});
});
