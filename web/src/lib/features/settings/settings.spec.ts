import { describe, expect, it } from 'vitest';
import {
	auditActionLabel,
	detailPairs,
	diffRows,
	groupAuditRows,
	isOpaqueId,
	localToRFC3339,
	parseWho,
	rangeSince,
	targetText
} from './audit';
import {
	deploymentFacts,
	FACTOR_POLICY,
	factorChangeConsequences,
	instanceNameProblem,
	settingsChanges,
	staySignedInConsequences
} from './model';
import { auditExportHref, cleanFilter, type AuditEvent, type SecuritySettings } from './queries';
import { settingsTabs } from './tabs';

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

	it('reads actions in words: known keys, catalog labels, then the key in words', () => {
		const catalog = {
			capabilities: [
				{ key: 'stack.deploy', label: 'Deploy', resourceType: 'stack' },
				{ key: 'stack.create', label: 'Create stacks', resourceType: 'stack' }
			],
			resourceTypes: [{ key: 'stack', label: 'Stacks' }]
		};
		expect(auditActionLabel('registry.use', catalog)).toBe('Used a registry connection');
		expect(auditActionLabel('auth.sign_in')).toBe('Signed in');
		expect(auditActionLabel('stack.deploy', catalog)).toBe('Deploy (stacks)');
		expect(auditActionLabel('stack.create', catalog)).toBe('Create stacks');
		expect(auditActionLabel('stack.migrate.preview')).toBe('Stack migrate preview');
	});

	it('names targets, never by an opaque ID', () => {
		const nameOf = (type: string, id: string) =>
			type === 'registry' && id === 'r1' ? 'Docker Hub' : undefined;
		expect(targetText({ type: 'registry', id: 'r1' }, nameOf)).toEqual({
			name: 'Docker Hub',
			type: 'Registry connection'
		});
		expect(
			targetText({ type: 'registry', id: '01a0daae-eed1-4c1f-9a2b-3c4d5e6f7a8b' }, nameOf)
		).toEqual({ name: 'Registry connection', type: 'Registry connection' });
		expect(targetText({ type: 'container', id: 'silo-web' })).toEqual({
			name: 'silo-web',
			type: 'Container'
		});
		expect(targetText({ type: 'build_run', id: 'x1' }).type).toBe('Build run');
		expect(isOpaqueId('sha256:0123456789abcdef')).toBe(true);
		expect(isOpaqueId('0123456789ab')).toBe(true);
		expect(isOpaqueId('silo-db')).toBe(false);
	});

	it('turns the Who and When filters into API filters', () => {
		expect(parseWho('user:u1')).toEqual({ actorId: 'u1' });
		expect(parseWho('kind:service')).toEqual({ actorKind: ['service'] });
		expect(parseWho('kind:nobody')).toEqual({});
		expect(parseWho('')).toEqual({});
		const now = Date.parse('2026-09-27T12:00:00Z');
		expect(rangeSince('24h', now)).toBe('2026-09-26T12:00:00.000Z');
		expect(rangeSince('7d', now)).toBe('2026-09-20T12:00:00.000Z');
		expect(rangeSince('', now)).toBeUndefined();
	});

	it('groups runs of identical records into one row', () => {
		const ev = (id: string, action: string, target = 'r1'): AuditEvent => ({
			id,
			action,
			actor: { kind: 'service' },
			at: '2026-09-27T12:00:00Z',
			category: 'credentials',
			details: {},
			hash: '',
			prevHash: '',
			outcome: 'success',
			seq: 1,
			targets: [{ type: 'registry', id: target }]
		});
		const rows = groupAuditRows([
			ev('1', 'registry.use'),
			ev('2', 'registry.use'),
			ev('3', 'registry.use'),
			ev('4', 'registry.use', 'r2'),
			ev('5', 'stack.deploy'),
			ev('6', 'registry.use')
		]);
		expect(rows.map((r) => [r.key, r.events.length])).toEqual([
			['1', 3],
			['4', 1],
			['5', 1],
			['6', 1]
		]);
	});
});

describe('sign-in policy (#16)', () => {
	const base: SecuritySettings = {
		allowStaySignedIn: true,
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
		expect(settingsChanges(base, { ...base, allowStaySignedIn: false })).toEqual([
			'Stay signed in: on → off'
		]);
	});

	it('says what turning Stay signed in off does to devices, and nothing otherwise', () => {
		expect(staySignedInConsequences(true, true)).toEqual([]);
		expect(staySignedInConsequences(false, true)).toEqual([]);
		expect(staySignedInConsequences(false, false)).toEqual([]);
		const c = staySignedInConsequences(true, false).join(' ');
		expect(c).toMatch(/stops offering Stay signed in/);
		expect(c).toMatch(/move back to the normal limits/);
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

describe('instance settings (#4)', () => {
	it('validates the display name like the server', () => {
		expect(instanceNameProblem('Homelab')).toBeNull();
		expect(instanceNameProblem('  Lab 2  ')).toBeNull();
		expect(instanceNameProblem('é'.repeat(64))).toBeNull();
		expect(instanceNameProblem('')).toBe('Enter a name.');
		expect(instanceNameProblem('   ')).toBe('Enter a name.');
		expect(instanceNameProblem('x'.repeat(65))).toBe('Use at most 64 characters.');
		expect(instanceNameProblem('a\nb')).toBe('Remove line breaks and control characters.');
		expect(instanceNameProblem('a\u0085b')).toBe('Remove line breaks and control characters.');
	});

	it('describes the read-only deployment configuration', () => {
		const facts = deploymentFacts({
			name: 'Docker Manager',
			instanceId: 'i',
			revision: 1,
			updatedAt: '2026-09-25T12:00:00Z',
			deployment: {
				publicUrl: 'http://localhost:8080',
				localDevelopment: true,
				trustedProxyCount: 0,
				streamHeartbeatSeconds: 15,
				filesMaxUploadBytes: 512 * 1024 ** 2,
				filesMaxEditBytes: 2 * 1024 ** 2,
				filesMaxDownloadBytes: 10 * 1024 ** 3,
				filesMaxExtractBytes: 20 * 1024 ** 3,
				filesMaxExtractRatio: 100,
				filesMaxArchiveEntries: 100000,
				metricsEndpoint: true
			}
		});
		expect(Object.fromEntries(facts.map((f) => [f.label, f.value]))).toEqual({
			'Public URL': 'http://localhost:8080',
			Mode: 'Local development over plain HTTP',
			'Trusted proxies': 'None: forwarded headers are ignored',
			'Stream heartbeat': 'Every 15 s',
			'Largest upload': '512 MB',
			'Largest file to edit': '2 MB',
			'Largest download or archive': '10 GB',
			'Largest extraction': '20 GB, at most 100× the archive, 100,000 entries',
			'Metrics endpoint': 'On'
		});
	});
});

describe('Settings tabs', () => {
	const access = (owner: boolean, ...caps: string[]) => ({
		owner,
		allowed: new Set(caps),
		environments: 0
	});

	it('shows the owner every instance setting, and nothing personal', () => {
		const tabs = settingsTabs(access(true));
		expect(tabs.map((t) => [t.label, t.href])).toEqual([
			['Overview', '/settings'],
			['API tokens', '/settings/tokens/all'],
			['Sign-in policy', '/settings/sign-in'],
			['Schedule defaults', '/settings/schedules'],
			['Notifications', '/settings/notifications'],
			['Audit log', '/settings/audit'],
			['Diagnostics', '/settings/diagnostics'],
			['Move to a new server', '/settings/move']
		]);
		// The caller's own account and tokens are the Profile, not Settings.
		expect(tabs.map((t) => t.label)).not.toContain('Profile');
		expect(tabs.map((t) => t.label)).not.toContain('Profile and security');
		expect(tabs.some((t) => t.href.startsWith('/profile'))).toBe(false);
	});

	it('hides the settings a member may not use', () => {
		expect(settingsTabs(access(false)).map((t) => t.label)).toEqual(['Overview']);
		expect(
			settingsTabs(access(false, 'settings.read', 'audit.read')).map((t) => t.label)
		).toEqual(['Overview', 'Schedule defaults', 'Audit log']);
	});
});
