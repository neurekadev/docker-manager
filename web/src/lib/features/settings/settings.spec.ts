import { describe, expect, it } from 'vitest';
import { detailPairs, diffRows, localToRFC3339 } from './audit';
import { FACTOR_POLICY, factorChangeConsequences, settingsChanges } from './model';
import { auditExportHref, cleanFilter, type SecuritySettings } from './queries';

describe('audit viewer (#30)', () => {
	it('shows rule diffs as added and removed rules', () => {
		const rows = diffRows({
			diff: {
				before: {
					revision: 3,
					rules: ['allow container.restart @environment:e1', 'allow stack.deploy @*']
				},
				after: {
					revision: 4,
					rules: ['allow stack.deploy @*', 'deny container.exec @container:e1/web']
				}
			},
			groupName: 'Operators'
		});
		expect(rows).toEqual([
			{ field: 'revision', before: '3', after: '4' },
			{
				field: 'rules',
				added: ['deny container.exec @container:e1/web'],
				removed: ['allow container.restart @environment:e1']
			}
		]);
		expect(detailPairs({ diff: {}, groupName: 'Operators', count: 2 })).toEqual([
			['groupName', 'Operators'],
			['count', '2']
		]);
	});

	it('handles creations and deletions (one side missing)', () => {
		expect(diffRows({ diff: { before: null, after: { name: 'Ops' } } })).toEqual([
			{ field: 'name', before: undefined, after: 'Ops' }
		]);
		expect(diffRows({})).toEqual([]);
	});

	it('builds filters and export links with repeated parameters', () => {
		const f = cleanFilter({
			action: ['stack.deploy', 'stack.stop'],
			outcome: [],
			actorId: '',
			since: '2026-09-01T00:00:00.000Z'
		});
		expect(f).toEqual({
			action: ['stack.deploy', 'stack.stop'],
			since: '2026-09-01T00:00:00.000Z'
		});
		expect(auditExportHref(f, 'csv')).toBe(
			'/api/v1/audit/exports?format=csv&action=stack.deploy&action=stack.stop&since=2026-09-01T00%3A00%3A00.000Z'
		);
		expect(localToRFC3339('')).toBeUndefined();
		expect(localToRFC3339('not a date')).toBeUndefined();
		expect(localToRFC3339('2026-09-25T10:00')).toMatch(/^2026-09-25T\d\d:00:00\.000Z$/);
	});
});

describe('sign-in policy (#16)', () => {
	const base: SecuritySettings = {
		apiTokenMaxLifetimeDays: 90,
		apiTokensEnabled: true,
		apiTokensNonExpiring: false,
		enrollmentGraceHours: 72,
		invitationTtlHours: 168,
		minPasswordLength: 15,
		passwordResetTtlHours: 24,
		requiredFactors: 'none',
		revision: 1,
		strictPasswords: true,
		updatedAt: ''
	};

	it('lists exactly what changes', () => {
		expect(settingsChanges(base, { ...base })).toEqual([]);
		expect(
			settingsChanges(base, { ...base, requiredFactors: 'both', apiTokensEnabled: false })
		).toEqual([
			'Required sign-in: Password or passkey → Authenticator app and passkey',
			'API tokens: on → off'
		]);
	});

	it('states the enrollment consequences of new required factors, never a lock-out', () => {
		expect(factorChangeConsequences('none', 'none', 72)).toEqual([]);
		const c = factorChangeConsequences('none', 'totp', 48);
		expect(c.join(' ')).toMatch(/Every other session ends/);
		expect(c.join(' ')).toMatch(/for 48 hours/);
		expect(c.join(' ')).toMatch(/owner has no deadline/);
		expect(Object.keys(FACTOR_POLICY)).toEqual(['none', 'totp', 'passkey', 'either', 'both']);
	});
});
