// Alert thresholds (Settings → Notifications, owner only): when a host's
// temperature, disk space or memory raises a warning or critical alert,
// as one set of defaults and an optional override per environment. A
// level is a whole number (°C up to 150, percent used up to 100), 0 turns
// it off, and a warning level must be below the critical one when both
// are on. An override's empty level uses the default; the manager checks
// every override with the defaults applied, so changing a default can
// make an override wrong. The forms keep the levels as typed (strings);
// this module checks them in words and builds the request. Pure
// (thresholds.spec.ts).
import type { Schema } from '$lib/api/client';
import { fieldError } from '$lib/ui/errors';

export type AlertSettings = Schema<'AlertSettings'>;
export type AlertThresholds = Schema<'AlertThresholds'>;
export type ThresholdOverride = Schema<'AlertThresholdOverride'>;
export type AlertSettingsBody = Schema<'PutAlertSettingsInputBody'>;
export type ThresholdKey = keyof AlertThresholds;

export interface ThresholdMetric {
	metric: 'temperature' | 'diskSpace' | 'memory';
	/** The grid's row: "Temperature (°C)". */
	label: string;
	/** In a sentence or a column: "Temperature". */
	short: string;
	/** After a value: " °C", "%". */
	unit: string;
	max: number;
	warning: ThresholdKey;
	critical: ThresholdKey;
}

/** The rows of the thresholds grid, in order. */
export const THRESHOLD_METRICS: ThresholdMetric[] = [
	{
		metric: 'temperature',
		label: 'Temperature (°C)',
		short: 'Temperature',
		unit: ' °C',
		max: 150,
		warning: 'temperatureWarning',
		critical: 'temperatureCritical'
	},
	{
		metric: 'diskSpace',
		label: 'Disk space (% used)',
		short: 'Disk space',
		unit: '%',
		max: 100,
		warning: 'diskSpaceWarning',
		critical: 'diskSpaceCritical'
	},
	{
		metric: 'memory',
		label: 'Memory (% used)',
		short: 'Memory',
		unit: '%',
		max: 100,
		warning: 'memoryWarning',
		critical: 'memoryCritical'
	}
];

/** Every level in the request's order. */
export const THRESHOLD_KEYS: ThresholdKey[] = THRESHOLD_METRICS.flatMap((m) => [
	m.warning,
	m.critical
]);

/** The levels as typed ('' for an empty field). */
export type ThresholdForm = Record<ThresholdKey, string>;
export type ThresholdErrors = Partial<Record<ThresholdKey, string>>;

/** The form of thresholds or an override (an absent level is empty). */
export function thresholdForm(t: Partial<Record<ThresholdKey, number>>): ThresholdForm {
	const out = {} as ThresholdForm;
	for (const k of THRESHOLD_KEYS) out[k] = t[k] === undefined ? '' : String(t[k]);
	return out;
}

/** Whether two forms hold the same levels (spaces around them ignored). */
export function sameForm(a: ThresholdForm, b: ThresholdForm): boolean {
	return THRESHOLD_KEYS.every((k) => a[k].trim() === b[k].trim());
}

/** A typed level: undefined when empty, null when it is no whole number from 0 to max. */
function parseLevel(v: string, max: number): number | undefined | null {
	const t = v.trim();
	if (t === '') return undefined;
	if (!/^\d+$/.test(t)) return null;
	const n = Number(t);
	return n <= max ? n : null;
}

const levelWords = (v: number, unit: string) => (v === 0 ? 'off' : `${v}${unit}`);

/**
 * What is wrong with the levels, per field, in words. Without `defaults`
 * every level is needed (the defaults themselves); with them an empty
 * level uses the default and the warning must be below the critical
 * level that applies.
 */
export function checkThresholds(form: ThresholdForm, defaults?: AlertThresholds): ThresholdErrors {
	const errors: ThresholdErrors = {};
	for (const m of THRESHOLD_METRICS) {
		const levels: Record<string, number | undefined> = {};
		for (const k of [m.warning, m.critical]) {
			const v = parseLevel(form[k] ?? '', m.max);
			if (v === null) errors[k] = `Enter a whole number from 0 to ${m.max}.`;
			else if (v === undefined && !defaults) errors[k] = 'Enter a level; 0 turns it off.';
			else levels[k] = v ?? defaults?.[k];
		}
		const w = levels[m.warning];
		const c = levels[m.critical];
		if (errors[m.warning] || errors[m.critical] || !w || !c || w < c) continue;
		const usesDefault = !!defaults && (form[m.critical] ?? '').trim() === '';
		errors[m.warning] = usesDefault
			? `Set it below the default critical level (${levelWords(c, m.unit)}).`
			: 'Set it below the critical level.';
	}
	return errors;
}

/** Whether there is nothing wrong. */
export function noErrors(errors: ThresholdErrors): boolean {
	return Object.keys(errors).length === 0;
}

/** The thresholds of a checked defaults form. */
export function thresholdsOf(form: ThresholdForm): AlertThresholds {
	const out = {} as AlertThresholds;
	for (const k of THRESHOLD_KEYS) out[k] = Number(form[k].trim());
	return out;
}

/** The override of a checked form: only the levels that are set. */
export function overrideOf(environmentId: string, form: ThresholdForm): ThresholdOverride {
	const out: ThresholdOverride = { environmentId };
	for (const k of THRESHOLD_KEYS) if (form[k].trim() !== '') out[k] = Number(form[k].trim());
	return out;
}

/** The override as a form (absent levels empty). */
export function overrideForm(o: ThresholdOverride | undefined): ThresholdForm {
	const levels: Partial<Record<ThresholdKey, number>> = {};
	for (const k of THRESHOLD_KEYS) if (o?.[k] !== undefined) levels[k] = o[k];
	return thresholdForm(levels);
}

/** The override's levels only (for the request: no other fields). */
function cleanOverride(o: ThresholdOverride): ThresholdOverride {
	const out: ThresholdOverride = { environmentId: o.environmentId };
	for (const k of THRESHOLD_KEYS) if (o[k] !== undefined) out[k] = o[k];
	return out;
}

/** The PUT body: the defaults and every override. */
export function settingsBody(
	thresholds: AlertThresholds,
	overrides: readonly ThresholdOverride[]
): AlertSettingsBody {
	const t = {} as AlertThresholds;
	for (const k of THRESHOLD_KEYS) t[k] = thresholds[k];
	return { thresholds: t, overrides: overrides.map(cleanOverride) };
}

/** The environments whose override would be wrong with these defaults (warning not below critical). */
export function brokenOverrides(
	overrides: readonly ThresholdOverride[],
	defaults: AlertThresholds
): string[] {
	return overrides
		.filter((o) => !noErrors(checkThresholds(overrideForm(o), defaults)))
		.map((o) => o.environmentId);
}

/**
 * The server's field errors on the levels: the defaults' (body.<level>)
 * or one override's (body.overrides[i].<level>).
 */
export function serverThresholdErrors(e: unknown, overrideIndex?: number): ThresholdErrors {
	const prefix = overrideIndex === undefined ? 'body.' : `body.overrides[${overrideIndex}].`;
	const out: ThresholdErrors = {};
	for (const k of THRESHOLD_KEYS) {
		const msg = fieldError(e, prefix + k);
		if (msg) out[k] = msg;
	}
	return out;
}

/** A level of an override in the list: "75 °C", "Off", "Default". */
export function levelText(v: number | undefined, unit: string): string {
	if (v === undefined) return 'Default';
	return v === 0 ? 'Off' : `${v}${unit}`;
}

/** An override's pair of levels: "75 °C / Default", or "Default" when it sets neither. */
export function overrideLevels(o: ThresholdOverride, m: ThresholdMetric): string {
	const w = o[m.warning];
	const c = o[m.critical];
	if (w === undefined && c === undefined) return 'Default';
	return `${levelText(w, m.unit)} / ${levelText(c, m.unit)}`;
}

/** The levels that apply with the override, in words (the cell's tooltip). */
export function overrideTitle(
	o: ThresholdOverride,
	m: ThresholdMetric,
	defaults: AlertThresholds
): string {
	const part = (k: ThresholdKey, name: string) => {
		const v = o[k] ?? defaults[k];
		const words = v === 0 ? `${name} off` : `${name} at ${v}${m.unit}`;
		return o[k] === undefined ? `${words} (default)` : words;
	};
	return `${m.short}: ${part(m.warning, 'warning')}, ${part(m.critical, 'critical')}`;
}
