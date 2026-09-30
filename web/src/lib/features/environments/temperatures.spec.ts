import { describe, expect, it } from 'vitest';
import type { EnvironmentMetrics } from '$lib/api/client';
import { rankColor } from '$lib/design/hue';
import { lastIndex, maxAt } from '$lib/ui/multiseries';
import { temperatureItems } from './temperatures';

const temp = (key: string, sensor: string, values: (number | null)[]) => ({
	key,
	unit: 'celsius' as const,
	sensor,
	values
});

const metrics = (series: EnvironmentMetrics['series']): EnvironmentMetrics => ({
	environmentId: 'env-1',
	from: '2026-09-30T12:00:00Z',
	to: '2026-09-30T12:03:00Z',
	stepSeconds: 60,
	resolution: '1m',
	timestamps: ['2026-09-30T12:00:00Z', '2026-09-30T12:01:00Z', '2026-09-30T12:02:00Z'],
	skewCorrected: false,
	incomplete: false,
	online: true,
	series
});

describe('temperatureItems (the Temperature chart)', () => {
	const m = metrics([
		{ key: 'cpu.percent', unit: 'percent', values: [10, 20, 30] },
		{ key: 'disk.used_bytes', unit: 'bytes', mount: 'docker', values: [1, 1, 1] },
		temp('temperature.celsius', 'acpitz', [27.8, 27.8, 27.8]),
		temp('temperature.celsius.max', 'acpitz', [28, 28, 28]),
		temp('temperature.celsius', 'coretemp: Package id 0', [45, 52.5, 48.5]),
		temp('temperature.celsius.max', 'coretemp: Package id 0', [61, 70, 55]),
		temp('temperature.celsius', 'nvme: Composite', [38.85, null, 40]),
		temp('temperature.celsius.max', 'nvme: Composite', [39, null, 41])
	]);

	it('makes one line per sensor from its averages, gaps kept as null', () => {
		const items = temperatureItems(m);
		expect(items.map((i) => i.name)).toEqual([
			'coretemp: Package id 0',
			'nvme: Composite',
			'acpitz'
		]);
		expect(items[0].values).toEqual([45, 52.5, 48.5]);
		expect(items[1].values).toEqual([38.85, null, 40]);
		// No parts: temperatures are not split.
		expect(items.every((i) => i.parts === undefined)).toBe(true);
	});

	it('ranks the sensors by their maximum over the range and colours them in that order', () => {
		// The package peaked at 70 °C, the drive at 41 °C, the ACPI zone at 28 °C.
		expect(temperatureItems(m).map((i) => i.color)).toEqual([
			rankColor(0, 3),
			rankColor(1, 3),
			rankColor(2, 3)
		]);
		// Without maximum series the averages rank them; ties go by name.
		const avgOnly = metrics([
			temp('temperature.celsius', 'b', [30, 50]),
			temp('temperature.celsius', 'c', [40, 40]),
			temp('temperature.celsius', 'a', [50, 20])
		]);
		expect(temperatureItems(avgOnly).map((i) => i.name)).toEqual(['a', 'b', 'c']);
	});

	it('heads the chart with the hottest sensor’s latest value', () => {
		const items = temperatureItems(m);
		const last = lastIndex(items);
		expect(last).toBe(2);
		expect(maxAt(items, last)).toBe(48.5);
	});

	it('has no items (no chart) when the host reports no sensors in the range', () => {
		expect(temperatureItems(undefined)).toEqual([]);
		expect(
			temperatureItems(metrics([{ key: 'cpu.percent', unit: 'percent', values: [1, 2, 3] }]))
		).toEqual([]);
		// A sensor with only gaps is left out too.
		expect(
			temperatureItems(metrics([temp('temperature.celsius', 'acpitz', [null, null, null])]))
		).toEqual([]);
	});
});
