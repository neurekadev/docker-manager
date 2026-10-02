import { describe, expect, it } from 'vitest';
import {
	candidateStatus,
	containerPolicy,
	containerTargetName,
	coveredTargets,
	daysText,
	imageLabel,
	inactiveReason,
	isClock,
	policiesByTarget,
	policySchedulesText,
	policyStatusText,
	publishedText,
	reasonLabel,
	recordPolicyHref,
	recoveryText,
	runnable,
	summarizeTargets,
	summaryState,
	summaryText,
	targetState,
	targetsUpdateText,
	updatesText,
	windowText,
	withExclusions,
	type UpdateCandidate,
	type UpdatePolicy
} from './model';

const cand = (c: Partial<UpdateCandidate>): UpdateCandidate => ({
	id: 'c',
	service: 'web',
	reference: 'ghcr.io/silo/web:latest',
	eligible: true,
	nonVersionTag: false,
	status: 'unchecked',
	...c
});

const policy = (summary?: UpdatePolicy['summary']): UpdatePolicy =>
	({
		id: 'p',
		name: 'Silo',
		environmentId: 'e1',
		target: { type: 'stack', id: 's1' },
		view: 'full',
		actions: [],
		summary
	}) as UpdatePolicy;

describe('update candidates', () => {
	it('presents every status with a tone and a sentence-case label', () => {
		expect(candidateStatus('update_available')).toEqual({
			tone: 'warn',
			label: 'Update available'
		});
		expect(candidateStatus('quarantined').tone).toBe('danger');
		expect(candidateStatus('up_to_date').label).toBe('Up to date');
		expect(candidateStatus('check_failed').label).toBe('Check failed');
	});

	it('explains ineligibility in the words of the definition', () => {
		expect(reasonLabel(cand({ reason: 'digest_pinned' }))).toBe('Pinned by @sha256 digest');
		expect(reasonLabel(cand({ reason: 'build_only' }))).toMatch(/Built from source/);
		expect(reasonLabel(cand({}))).toBeNull();
	});

	it('runs only eligible candidates with an update available', () => {
		const list = [
			cand({ id: 'a', status: 'update_available' }),
			cand({ id: 'b', status: 'update_available', eligible: false }),
			cand({ id: 'c', status: 'quarantined' }),
			cand({ id: 'd', status: 'up_to_date' })
		];
		expect(runnable(list).map((c) => c.id)).toEqual(['a']);
	});

	it('guides manual recovery by pinning the previous digest in the user’s own source', () => {
		const text = recoveryText(
			cand({
				repository: 'silo/api',
				reference: 'ghcr.io/silo/api:2.4',
				previousDigest: 'sha256:abc'
			})
		);
		expect(text).toContain('silo/api@sha256:abc');
		expect(text).toContain('never edits your files');
		expect(recoveryText(cand({ guidance: 'Server says so.' }))).toBe('Server says so.');
	});
});

describe('policy summary', () => {
	it('puts quarantine and failures before available updates', () => {
		const base = {
			available: 2,
			failed: 0,
			ineligible: 0,
			quarantined: 0,
			unchecked: 0,
			upToDate: 1
		};
		expect(summaryText(policy({ ...base, quarantined: 1 }))).toEqual({
			text: '1 quarantined',
			tone: 'danger'
		});
		expect(summaryText(policy({ ...base, failed: 3 })).text).toBe('3 failed');
		expect(summaryText(policy(base))).toEqual({ text: '2 updates available', tone: 'warn' });
		expect(summaryText(policy({ ...base, available: 1 })).text).toBe('1 update available');
		expect(
			summaryText(policy({ ...base, available: 0, lastCheckAt: '2026-09-25T00:00:00Z' }))
		).toEqual({
			text: 'Up to date',
			tone: 'ok'
		});
		expect(summaryText(policy(undefined)).text).toBe('Not checked yet');
	});
});

describe('update window', () => {
	it('collapses consecutive days and spans midnight as given', () => {
		expect(daysText([1, 2, 3, 4, 5])).toBe('Mon–Fri');
		expect(daysText([0, 6])).toBe('Sun, Sat');
		expect(daysText([1, 2, 4])).toBe('Mon, Tue, Thu');
		expect(windowText({ days: [1, 2, 3, 4, 5], start: '01:00', end: '05:00' })).toBe(
			'Mon–Fri, 01:00–05:00'
		);
		expect(windowText({ start: '23:00', end: '02:00' })).toBe('Every day, 23:00–02:00');
		expect(windowText(undefined)).toBe('Any time');
	});

	it('accepts 24-hour clock times only', () => {
		expect(isClock('00:00')).toBe(true);
		expect(isClock('23:59')).toBe(true);
		expect(isClock('24:00')).toBe(false);
		expect(isClock('7:00')).toBe(false);
	});
});

describe('target summaries', () => {
	const sum = (s: Partial<Parameters<typeof summaryState>[0] & object>) => ({
		available: 0,
		failed: 0,
		ineligible: 0,
		quarantined: 0,
		unchecked: 0,
		upToDate: 0,
		...s
	});
	const at = '2026-09-25T12:00:00Z';

	it('ranks exclusions, failures, updates, unchecked and up to date', () => {
		expect(summaryState(sum({ available: 2 }), true).label).toBe('Excluded');
		expect(
			summaryState(sum({ failed: 1, quarantined: 1, available: 3, lastCheckAt: at }))
		).toEqual({
			tone: 'danger',
			label: '2 failures'
		});
		expect(summaryState(sum({ available: 1, lastCheckAt: at }))).toEqual({
			tone: 'warn',
			label: '1 update available'
		});
		expect(summaryState(sum({})).label).toBe('Not checked yet');
		expect(summaryState(undefined).label).toBe('Not checked yet');
		expect(summaryState(sum({ upToDate: 2, lastCheckAt: at })).tone).toBe('ok');
	});

	it('counts targets by state and keeps the newest check', () => {
		const out = summarizeTargets([
			sum({ available: 1, lastCheckAt: '2026-09-24T00:00:00Z' }),
			sum({ failed: 1, lastCheckAt: at }),
			sum({ upToDate: 3, lastCheckAt: '2026-09-20T00:00:00Z' }),
			sum({}),
			undefined
		]);
		expect(out).toEqual({
			withUpdates: 1,
			images: 1,
			failing: 1,
			upToDate: 1,
			unchecked: 2,
			lastCheckAt: at
		});
	});
});

describe('update counts and coverage (#20)', () => {
	it('names the images and where they are', () => {
		expect(updatesText(6, { stacks: 5, containers: 0 })).toBe('6 images in 5 stacks');
		expect(updatesText(1, { stacks: 0, containers: 1 })).toBe('1 image in 1 container');
		expect(updatesText(3, { stacks: 1, containers: 1 })).toBe(
			'3 images in 2 stacks and containers'
		);
		expect(updatesText(0, { stacks: 0, containers: 0 })).toBe('Nothing waiting');
	});

	it('counts the images of the stacks and containers with updates', () => {
		const at = '2026-09-25T12:00:00Z';
		const s = (available: number, failed = 0) => ({
			available,
			failed,
			ineligible: 0,
			quarantined: 0,
			unchecked: 0,
			upToDate: 0,
			lastCheckAt: at
		});
		expect(
			targetsUpdateText([
				{ type: 'stack', candidateSummary: s(2) },
				{ type: 'stack', candidateSummary: s(1) },
				// A failing target counts as failing, not as an update.
				{ type: 'stack', candidateSummary: s(4, 1) },
				{ type: 'stack', candidateSummary: s(0) }
			])
		).toBe('3 images in 2 stacks');
	});

	it('keeps covered targets once, in the selected environment', () => {
		const t = (policyId: string, environmentId: string, inactive = false) => ({
			policyId,
			environmentId,
			inactive
		});
		const lists = [[t('a', 'e1'), t('b', 'e2'), t('c', 'e1', true)], [t('a', 'e1')], undefined];
		expect(coveredTargets(lists, null).map((x) => x.policyId)).toEqual(['a', 'b']);
		expect(coveredTargets(lists, 'e2').map((x) => x.policyId)).toEqual(['b']);
	});

	it('names containers, never by their Engine ID', () => {
		const id = 'a'.repeat(64);
		const list = [{ id, name: 'pihole' }];
		expect(containerTargetName('pihole', list)).toEqual({ name: 'pihole', found: true });
		expect(containerTargetName(id, list)).toEqual({ name: 'pihole', found: true });
		expect(containerTargetName('b'.repeat(64), list).name).toBe('Removed container');
		expect(containerTargetName('nginx')).toEqual({ name: 'nginx', found: true });
		expect(containerTargetName('nginx', list).found).toBe(false);
	});

	it("takes the manager's reason for targets it no longer covers", () => {
		expect(inactiveReason({ inactive: false, inactiveReason: 'excluded' })).toBeNull();
		expect(inactiveReason({ inactive: true, inactiveReason: 'excluded' })).toBe('excluded');
		expect(inactiveReason({ inactive: true, inactiveReason: 'missing' })).toBe('missing');
		expect(inactiveReason({ inactive: true })).toBe('missing');
		expect(targetState(undefined, 'missing').label).toBe('No longer found');
		expect(targetState(undefined, 'excluded').label).toBe('Excluded');
		expect(targetState(undefined, null).label).toBe('Not checked yet');
	});

	it('says when a newer image was published', () => {
		const now = new Date('2026-09-25T10:00:00Z');
		expect(publishedText({ publishedAt: '2026-09-22T10:00:00Z' }, now)).toBe(
			'published 3 days ago'
		);
		expect(publishedText({}, now)).toBeNull();
		expect(publishedText({ publishedAt: null }, now)).toBeNull();
	});

	it('lists exclusions that never got a target record', () => {
		const empty = {
			available: 0,
			upToDate: 0,
			quarantined: 0,
			ineligible: 0,
			failed: 0,
			unchecked: 0
		};
		const known = {
			policyId: 'p1',
			environmentId: 'e1',
			type: 'stack' as const,
			id: 's1',
			inactive: true,
			candidateSummary: empty
		};
		const rows = withExclusions(
			[known],
			{
				scope: 'all',
				excludeStacks: ['s1', 's2', 'gone'],
				excludeContainers: ['e2/pihole']
			},
			(id) => (id === 'gone' ? undefined : 'e1')
		);
		expect(rows.map((r) => `${r.environmentId}/${r.type}/${r.id}`)).toEqual([
			'e1/stack/s1',
			'e1/stack/s2',
			'e2/container/pihole'
		]);
		expect(rows.every((r) => r.inactive)).toBe(true);
		expect(rows.slice(1).every((r) => r.inactiveReason === 'excluded')).toBe(true);
	});

	it('says what an update policy needs in one sentence', () => {
		const t = (x: Partial<ReturnType<typeof summarizeTargets>>) => ({
			withUpdates: 0,
			images: 0,
			failing: 0,
			upToDate: 0,
			unchecked: 0,
			...x
		});
		expect(policyStatusText(t({}), 0, '')).toBe('Nothing in scope to update yet.');
		expect(policyStatusText(t({ unchecked: 3 }), 3, '')).toMatch(/^Not checked yet/);
		expect(policyStatusText(t({ withUpdates: 5 }), 20, '6 images in 5 stacks')).toBe(
			'Newer images: 6 images in 5 stacks. Preview them to update.'
		);
		expect(policyStatusText(t({ failing: 1 }), 20, '')).toBe(
			'1 stack or container failed the last check or update.'
		);
		expect(policyStatusText(t({ upToDate: 20 }), 20, '')).toBe(
			'Everything it covers is up to date.'
		);
	});

	it('reads both schedules of a policy in one sentence', () => {
		const on = (cron: string, timeZone = 'UTC') => ({ cron, timeZone, enabled: true });
		const off = { cron: '0 4 * * *', timeZone: 'UTC', enabled: false };
		expect(policySchedulesText(on('0 * * * *'), on('0 4 * * *'), 'Europe/Berlin')).toBe(
			'Checks every hour, updates daily at 04:00 (UTC)'
		);
		expect(policySchedulesText(on('0 3 * * *'), off, 'UTC')).toBe(
			'Checks daily at 03:00, updates only by hand'
		);
		expect(policySchedulesText(off, off, 'UTC')).toBe('Checks and updates only by hand');
	});

	it('shows images without the implied Docker Hub prefix', () => {
		expect(imageLabel({ reference: 'docker.io/library/nginx:1.27' })).toBe('nginx:1.27');
		expect(imageLabel({ reference: 'ghcr.io/silo/web:latest' })).toBe(
			'ghcr.io/silo/web:latest'
		);
	});
});

describe('update badges (#20)', () => {
	const index = policiesByTarget([
		{
			id: 'p1',
			environmentId: 'e1',
			target: { type: 'stack', id: 's1' },
			actions: ['update.check']
		},
		{ id: 'p2', environmentId: 'e1', target: { type: 'container', id: 'pihole' }, actions: [] }
	]);

	it("finds a container's policy through its stack or its own name", () => {
		expect(
			containerPolicy(index, { environmentId: 'e1', name: 'web', stack: { stackId: 's1' } })
		).toEqual({ id: 'p1', canCheck: true });
		expect(containerPolicy(index, { environmentId: 'e1', name: 'pihole' })).toEqual({
			id: 'p2',
			canCheck: false
		});
		expect(containerPolicy(index, { environmentId: 'e2', name: 'pihole' })).toBeUndefined();
		// A Compose project Docker Manager does not manage has no stack ID: no policy.
		expect(containerPolicy(index, { environmentId: 'e1', name: 'pihole', stack: {} })).toBe(
			undefined
		);
	});
});

describe('links to a target record (#218)', () => {
	it('lead to the environment policy that manages it, else Updates', () => {
		expect(recordPolicyHref({ id: 'rec-1', parentId: 'pol-1' })).toBe('/updates/pol-1');
		expect(recordPolicyHref({ id: 'rec-1' })).toBe('/updates');
		// Never back to itself.
		expect(recordPolicyHref({ id: 'rec-1', parentId: 'rec-1' })).toBe('/updates');
	});
});
