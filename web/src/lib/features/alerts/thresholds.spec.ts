import { describe, expect, it } from 'vitest';
import { ApiRequestError } from '$lib/api/client';
import {
	THRESHOLD_KEYS,
	THRESHOLD_METRICS,
	brokenOverrides,
	checkThresholds,
	levelText,
	noErrors,
	overrideForm,
	overrideLevels,
	overrideOf,
	overrideTitle,
	sameForm,
	serverThresholdErrors,
	settingsBody,
	thresholdForm,
	thresholdsOf,
	type AlertThresholds
} from './thresholds';

const defaults: AlertThresholds = {
	temperatureWarning: 80,
	temperatureCritical: 90,
	diskSpaceWarning: 85,
	diskSpaceCritical: 95,
	memoryWarning: 90,
	memoryCritical: 95
};

const temperature = THRESHOLD_METRICS[0];

describe('alert thresholds', () => {
	it('has a row per metric with its unit and limit', () => {
		expect(THRESHOLD_METRICS.map((m) => [m.label, m.max])).toEqual([
			['Temperature (°C)', 150],
			['Disk Space (% Used)', 100],
			['Memory (% Used)', 100]
		]);
		expect(THRESHOLD_KEYS).toEqual([
			'temperatureWarning',
			'temperatureCritical',
			'diskSpaceWarning',
			'diskSpaceCritical',
			'memoryWarning',
			'memoryCritical'
		]);
	});

	it('round-trips the defaults through the form', () => {
		const form = thresholdForm(defaults);
		expect(form.temperatureWarning).toBe('80');
		expect(noErrors(checkThresholds(form))).toBe(true);
		expect(thresholdsOf({ ...form, memoryWarning: ' 70 ' })).toEqual({
			...defaults,
			memoryWarning: 70
		});
		expect(sameForm(form, { ...form, memoryWarning: '90 ' })).toBe(true);
		expect(sameForm(form, { ...form, memoryWarning: '91' })).toBe(false);
	});

	it('checks the defaults: whole numbers in range, every level, warning below critical', () => {
		const form = thresholdForm(defaults);
		expect(checkThresholds({ ...form, temperatureWarning: '151' })).toEqual({
			temperatureWarning: 'Enter a whole number from 0 to 150.'
		});
		expect(checkThresholds({ ...form, diskSpaceCritical: '101' })).toEqual({
			diskSpaceCritical: 'Enter a whole number from 0 to 100.'
		});
		expect(checkThresholds({ ...form, memoryWarning: '1.5' })).toEqual({
			memoryWarning: 'Enter a whole number from 0 to 100.'
		});
		expect(checkThresholds({ ...form, memoryWarning: '-1' })).toEqual({
			memoryWarning: 'Enter a whole number from 0 to 100.'
		});
		expect(checkThresholds({ ...form, memoryCritical: '' })).toEqual({
			memoryCritical: 'Enter a level; 0 turns it off.'
		});
		expect(checkThresholds({ ...form, diskSpaceWarning: '95' })).toEqual({
			diskSpaceWarning: 'Set it below the critical level.'
		});
		// 0 turns a level off: no order to keep.
		expect(
			checkThresholds({ ...form, diskSpaceWarning: '95', diskSpaceCritical: '0' })
		).toEqual({});
		expect(checkThresholds({ ...form, temperatureWarning: '0' })).toEqual({});
	});

	it('checks an override with the defaults applied', () => {
		const empty = overrideForm(undefined);
		expect(noErrors(checkThresholds(empty, defaults))).toBe(true);
		expect(checkThresholds({ ...empty, temperatureWarning: '95' }, defaults)).toEqual({
			temperatureWarning: 'Set it below the default critical level (90 °C).'
		});
		expect(
			checkThresholds(
				{ ...empty, temperatureWarning: '95', temperatureCritical: '99' },
				defaults
			)
		).toEqual({});
		expect(checkThresholds({ ...empty, memoryCritical: '80' }, defaults)).toEqual({
			memoryWarning: 'Set it below the critical level.'
		});
		expect(checkThresholds({ ...empty, memoryCritical: 'x' }, defaults)).toEqual({
			memoryCritical: 'Enter a whole number from 0 to 100.'
		});
	});

	it('sends only the levels an override sets, and every override', () => {
		const form = { ...overrideForm(undefined), temperatureWarning: '70', memoryCritical: '0' };
		const o = overrideOf('e1', form);
		expect(o).toEqual({ environmentId: 'e1', temperatureWarning: 70, memoryCritical: 0 });
		expect(overrideForm(o)).toEqual(form);
		expect(
			settingsBody({ ...defaults, revision: 3 } as AlertThresholds, [
				{ ...o, extra: true } as never
			])
		).toEqual({ thresholds: defaults, overrides: [o] });
	});

	it('finds overrides new defaults would break', () => {
		const overrides = [
			{ environmentId: 'e1', temperatureCritical: 85 },
			{ environmentId: 'e2', memoryWarning: 50 }
		];
		expect(brokenOverrides(overrides, defaults)).toEqual([]);
		expect(brokenOverrides(overrides, { ...defaults, temperatureWarning: 88 })).toEqual(['e1']);
	});

	it('shows an override’s levels compactly, with what applies as tooltip', () => {
		expect(levelText(undefined, '%')).toBe('Default');
		expect(levelText(0, '%')).toBe('Off');
		expect(levelText(75, ' °C')).toBe('75 °C');
		expect(overrideLevels({ environmentId: 'e1' }, temperature)).toBe('Default');
		expect(overrideLevels({ environmentId: 'e1', temperatureWarning: 75 }, temperature)).toBe(
			'75 °C / Default'
		);
		expect(
			overrideTitle({ environmentId: 'e1', temperatureWarning: 0 }, temperature, defaults)
		).toBe('Temperature: warning off, critical at 90 °C (default)');
	});

	it('places the server’s field errors on the defaults or one override', () => {
		const err = new ApiRequestError('invalid alert thresholds', 422, {
			code: 'validation_failed',
			message: 'invalid alert thresholds',
			requestId: 'r1',
			retryable: false,
			details: [
				{
					field: 'body.overrides[1].memoryWarning',
					message: 'Must be below the critical level.'
				}
			]
		});
		expect(serverThresholdErrors(err, 1)).toEqual({
			memoryWarning: 'Must be below the critical level.'
		});
		expect(serverThresholdErrors(err, 0)).toEqual({});
		expect(serverThresholdErrors(err)).toEqual({});
	});
});
