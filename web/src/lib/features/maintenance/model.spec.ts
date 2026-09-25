import { describe, expect, it } from 'vitest';
import {
	CATEGORIES,
	ageText,
	enabledRules,
	joinAge,
	normalizeRules,
	ruleProblem,
	ruleSummary,
	runSummaryText,
	splitAge
} from './model';

describe('prune rules', () => {
	it('always has one rule per category, missing ones off with 30 days (#14 safe defaults)', () => {
		const rules = normalizeRules([
			{ category: 'dangling_images', enabled: true, minAgeHours: 24 }
		]);
		expect(rules.map((r) => r.category)).toEqual(CATEGORIES);
		expect(rules.find((r) => r.category === 'dangling_images')).toMatchObject({
			enabled: true,
			minAgeHours: 24
		});
		for (const r of rules.filter((r) => r.category !== 'dangling_images'))
			expect(r).toMatchObject({ enabled: false, minAgeHours: 720 });
		expect(enabledRules({ rules }).map((r) => r.category)).toEqual(['dangling_images']);
	});

	it('orders categories from the least to the most destructive, volumes last', () => {
		expect(CATEGORIES.slice(-2)).toEqual(['anonymous_volumes', 'named_volumes']);
	});

	it('refuses an enabled volume rule without its own opt-in', () => {
		expect(ruleProblem({ category: 'named_volumes', enabled: true, minAgeHours: 720 })).toMatch(
			/deletes their data/
		);
		expect(
			ruleProblem({
				category: 'named_volumes',
				enabled: true,
				minAgeHours: 720,
				volumeOptIn: true
			})
		).toBeNull();
		expect(
			ruleProblem({ category: 'named_volumes', enabled: false, minAgeHours: 720 })
		).toBeNull();
	});

	it('refuses label filters on build cache and negative ages', () => {
		expect(
			ruleProblem({
				category: 'build_cache',
				enabled: true,
				minAgeHours: 0,
				includeLabels: ['a=b']
			})
		).toMatch(/no labels/);
		expect(
			ruleProblem({ category: 'stopped_containers', enabled: true, minAgeHours: -1 })
		).toMatch(/whole number/);
	});

	it('describes rules in plain words', () => {
		expect(ageText(720)).toBe('older than 30 days');
		expect(ageText(24)).toBe('older than 1 day');
		expect(ageText(12)).toBe('older than 12 hours');
		expect(ageText(0)).toBe('any age');
		expect(
			ruleSummary({
				category: 'stopped_containers',
				enabled: true,
				minAgeHours: 720,
				exclude: ['a', 'b']
			})
		).toBe('Stopped containers older than 30 days, 2 excluded');
		expect(
			ruleSummary({
				category: 'build_cache',
				enabled: true,
				minAgeHours: 48,
				buildCacheAll: true,
				keepStorageBytes: 2 * 1024 ** 3
			})
		).toBe('Build cache older than 2 days, all unused records, keeping 2 GB');
	});

	it('edits ages in days when they are whole days', () => {
		expect(splitAge(720)).toEqual({ value: 30, unit: 'days' });
		expect(splitAge(36)).toEqual({ value: 36, unit: 'hours' });
		expect(splitAge(0)).toEqual({ value: 0, unit: 'hours' });
		expect(joinAge(7, 'days')).toBe(168);
		expect(joinAge(7, 'hours')).toBe(7);
	});

	it('summarizes a finished run', () => {
		expect(
			runSummaryText({
				bytesReclaimed: 1024 * 1024 * 5,
				deferred: 0,
				failed: 1,
				finishedAt: '2026-09-25T00:00:00Z',
				jobId: 'j',
				origin: 'manual',
				removed: 3,
				skipped: 0,
				state: 'partial'
			})
		).toBe('Removed 3, 1 failed, 5 MB reclaimed');
	});
});
