import { describe, expect, it } from 'vitest';
import type { MyPermissions, SearchHit } from '$lib/api/client';
import { EnvironmentSelection, type StorageLike } from './environment.svelte';
import {
	ACCOUNT_ITEMS,
	accessOf,
	activeNav,
	isRestricted,
	NAV_GROUPS,
	NAV_ITEMS,
	palettePages,
	visibleNav
} from './nav';
import * as noticesModule from './notices.svelte';
import {
	alertDismissKey,
	DISMISSED_KEY,
	isGeneratedPolicyName,
	jobNotices,
	MAX_DISMISSED,
	noticeHref,
	Notices,
	parseDismissed,
	policyLabel
} from './notices.svelte';
import {
	actionResults,
	grouped,
	hitResults,
	hrefForHit,
	pageResults,
	recentResults
} from './palette';
import { RecentPages } from './recent.svelte';
import { bannerDelay, bannerText, indicatorText, OFFLINE_BANNER_DELAY_MS } from './live-banner';

function perms(p: Partial<MyPermissions>): MyPermissions {
	return {
		owner: false,
		catalogVersion: 1,
		entries: [],
		environments: [],
		...p
	} as MyPermissions;
}

function allow(capability: string, allowed = true) {
	return {
		capability,
		allowed,
		source: 'group_rule',
		reason: '',
		scope: { kind: 'instance' }
	} as MyPermissions['entries'][number];
}

describe('navigation filter (#17)', () => {
	it('shows a Restricted user only the dashboard and settings', () => {
		const a = accessOf(perms({}));
		expect(isRestricted(a)).toBe(true);
		expect(visibleNav(a).map((i) => i.id)).toEqual(['dashboard', 'settings']);
	});

	it('shows exactly the sections a grant reaches; denials show nothing', () => {
		const a = accessOf(
			perms({
				entries: [
					allow('container.metrics.read'),
					allow('stack.read', false),
					allow('backup.run')
				],
				environments: [{ id: 'e1', name: 'homelab', view: 'minimal', actions: [] }]
			})
		);
		expect(isRestricted(a)).toBe(false);
		expect(visibleNav(a).map((i) => i.id)).toEqual([
			'dashboard',
			'environments',
			'notifications',
			'containers',
			'backups',
			'jobs',
			'settings'
		]);
	});

	it('shows everything to the owner, including access administration', () => {
		const ids = visibleNav(accessOf(perms({ owner: true }))).map((i) => i.id);
		expect(ids).toContain('access');
		expect(ids).toContain('registries');
		expect(ids).toContain('templates');
		expect(ids).toContain('schedules');
		expect(ids).toContain('notifications');
		expect(ids).toHaveLength(18);
	});

	it('shows Schedules, after Jobs, to readers of scheduled policies only', () => {
		const reader = visibleNav(accessOf(perms({ entries: [allow('update_policy.read')] }))).map(
			(i) => i.id
		);
		expect(reader.slice(reader.indexOf('jobs'), reader.indexOf('jobs') + 2)).toEqual([
			'jobs',
			'schedules'
		]);
		const runner = visibleNav(accessOf(perms({ entries: [allow('update.run')] }))).map(
			(i) => i.id
		);
		expect(runner).not.toContain('schedules');
		expect(activeNav('/schedules')?.id).toBe('schedules');
	});

	it('puts every item in a group, labelled after the first', () => {
		const ids = NAV_GROUPS.map((g) => g.id);
		for (const i of NAV_ITEMS) expect(ids, i.id).toContain(i.group);
		expect(NAV_GROUPS[0].label).toBeUndefined();
		expect(NAV_GROUPS.slice(1).map((g) => g.label)).toEqual([
			'Docker',
			'Automation',
			'Administration'
		]);
		// Every section has its own icon (Registries no longer shares one).
		expect(new Set(NAV_ITEMS.map((i) => i.icon)).size).toBe(NAV_ITEMS.length);
	});

	it('finds the active section, including environment-scoped details', () => {
		expect(activeNav('/')?.id).toBe('dashboard');
		expect(activeNav('/stacks/abc/files')?.id).toBe('stacks');
		expect(activeNav('/environments/e1')?.id).toBe('environments');
		expect(activeNav('/environments/e1/containers/c9')?.id).toBe('containers');
		expect(activeNav('/environments/e1/volumes/data')?.id).toBe('volumes');
		expect(activeNav('/containers/e1/web/logs')?.id).toBe('containers');
		expect(activeNav('/volumes/e1/data/files')?.id).toBe('volumes');
		expect(activeNav('/builds/e1/b1')?.id).toBe('builds');
		expect(activeNav('/settings/tokens/all')?.id).toBe('settings');
		expect(activeNav('/notifications')?.id).toBe('notifications');
		// The channels stay in Settings.
		expect(activeNav('/settings/notifications')?.id).toBe('settings');
		expect(activeNav('/nowhere')).toBeUndefined();
	});

	it('keeps the personal Profile out of the sidebar and out of Settings', () => {
		const owner = accessOf(perms({ owner: true }));
		expect(visibleNav(owner).map((i) => i.id)).not.toContain('profile');
		expect(NAV_GROUPS.map((g) => g.id)).not.toContain('account');
		expect(ACCOUNT_ITEMS.map((i) => [i.id, i.label, i.href])).toEqual([
			['profile', 'Profile', '/profile']
		]);
		// No sidebar section lights up on the Profile pages.
		expect(activeNav('/profile')).toBeUndefined();
		expect(activeNav('/profile/tokens/new')).toBeUndefined();
	});
});

class MemStorage implements StorageLike {
	m = new Map<string, string>();
	getItem(k: string) {
		return this.m.get(k) ?? null;
	}
	setItem(k: string, v: string) {
		this.m.set(k, v);
	}
	removeItem(k: string) {
		this.m.delete(k);
	}
}

describe('environment selection (remembered per user, ID only)', () => {
	it('persists per user and falls back to all environments when the ID is gone', () => {
		const store = new MemStorage();
		const sel = new EnvironmentSelection(store);
		sel.restore('u1', ['e1', 'e2']);
		expect(sel.id).toBeNull();
		sel.select('e2');
		expect(store.m.get('docker-manager:environment:u1')).toBe('e2');

		const again = new EnvironmentSelection(store);
		again.restore('u1', ['e1', 'e2']);
		expect(again.id).toBe('e2');
		// Another user has their own choice.
		const other = new EnvironmentSelection(store);
		other.restore('u2', ['e1', 'e2']);
		expect(other.id).toBeNull();
		// Access to e2 revoked: back to all environments.
		const revoked = new EnvironmentSelection(store);
		revoked.restore('u1', ['e1']);
		expect(revoked.id).toBeNull();
		again.select(null);
		expect(store.m.has('docker-manager:environment:u1')).toBe(false);
		// Only the ID is stored.
		expect([...store.m.values()].every((v) => /^[\w-]+$/.test(v))).toBe(true);
	});

	it('works without storage', () => {
		const sel = new EnvironmentSelection(null);
		sel.restore('u1', ['e1']);
		sel.select('e1');
		expect(sel.id).toBe('e1');
		sel.reset();
		expect(sel.id).toBeNull();
	});
});

describe('notices', () => {
	const alert = (
		id: string,
		o: Partial<{
			severity: 'critical' | 'warning' | 'info';
			escalation: number;
			title: string;
			actions: string[];
			startedAt: string;
			link: string;
		}> = {}
	) => ({
		id,
		severity: o.severity ?? 'warning',
		escalation: o.escalation ?? 0,
		title: o.title ?? `Alert ${id}`,
		link: o.link ?? `/environments/e1?tab=system`,
		startedAt: o.startedAt ?? '2026-09-25T12:00:00Z',
		actions: o.actions ?? []
	});

	it('deduplicates job notices by key and counts what is not dismissed', () => {
		let t = 0;
		const n = new Notices(() => ++t, null);
		n.push({ key: 'job:1', kind: 'job', tone: 'ok', title: 'Deployed Silo' });
		n.push({ key: 'job:2', kind: 'job', tone: 'danger', title: 'Deploy of Media failed' });
		n.push({ key: 'job:1', kind: 'job', tone: 'ok', title: 'Deployed Silo again' });
		expect(n.items.map((x) => x.title)).toEqual([
			'Deployed Silo again',
			'Deploy of Media failed'
		]);
		expect(n.count).toBe(2);
		n.resolve('job:2');
		expect(n.items).toHaveLength(1);
		expect(n.count).toBe(1);
	});

	it('lists the active alerts first, critical first, instead of browser-made notices', () => {
		const n = new Notices(() => 1, null);
		n.push({ key: 'job:1', kind: 'job', tone: 'ok', title: 'Deployed Silo', href: '/jobs/1' });
		n.setAlerts([
			alert('a1', { title: 'edge is offline', link: '/environments/e2' }),
			alert('a2', {
				severity: 'critical',
				title: 'Disk /dev/sda on homelab is failing',
				actions: ['alert.dismiss'],
				startedAt: '2026-09-20T12:00:00Z'
			}),
			alert('a3', { title: '2 updates available for Silo', link: '/updates/p1' })
		]);
		expect(n.list.map((x) => [x.key, x.kind, x.tone, x.title])).toEqual([
			['alert:a2', 'alert', 'danger', 'Disk /dev/sda on homelab is failing'],
			['alert:a1', 'alert', 'warn', 'edge is offline'],
			['alert:a3', 'alert', 'warn', '2 updates available for Silo'],
			['job:1', 'job', 'ok', 'Deployed Silo']
		]);
		expect(n.list[1].href).toBe('/environments/e2');
		expect(n.list.map((x) => x.serverDismiss)).toEqual([true, false, false, false]);
		expect(n.count).toBe(4);
		// The manager's alerts replace the offline and update notices the
		// browser used to compute.
		expect(Object.keys(noticesModule)).not.toContain('environmentNotices');
		expect(Object.keys(noticesModule)).not.toContain('updateNotices');
	});

	it('keeps the count until items are dismissed, and keeps dismissals across reloads', () => {
		const store = new MemStorage();
		const n = new Notices(() => 1, store);
		n.push({ key: 'job:1', kind: 'job', tone: 'ok', title: 'Deployed Silo' });
		n.setAlerts([alert('a1'), alert('a2')]);
		expect(n.count).toBe(3);
		n.dismiss('job:1', alertDismissKey(alert('a1')));
		expect(n.list.map((x) => x.key)).toEqual(['alert:a2']);
		expect(JSON.parse(store.m.get(DISMISSED_KEY) ?? '[]')).toEqual(['job:1', 'alert:a1:0']);

		// A new tab (or a reload) reads them back.
		const again = new Notices(() => 1, store);
		again.push({ key: 'job:1', kind: 'job', tone: 'ok', title: 'Deployed Silo' });
		again.setAlerts([alert('a1'), alert('a2')]);
		expect(again.list.map((x) => x.key)).toEqual(['alert:a2']);
		// A quiet change (progress, counters) keeps it hidden.
		again.setAlerts([alert('a1', { title: 'Alert a1, 47% rebuilt' }), alert('a2')]);
		expect(again.list.map((x) => x.key)).toEqual(['alert:a2']);
		// An alert that gets worse shows again, also at the same severity
		// (a new problem: the manager's escalation counts up).
		again.setAlerts([alert('a1', { escalation: 1 }), alert('a2')]);
		expect(again.list.map((x) => x.key)).toEqual(['alert:a1', 'alert:a2']);
		// Clearing (sign-out) forgets the items, not the dismissals.
		again.clear();
		expect(again.count).toBe(0);
		expect(again.isDismissed('job:1')).toBe(true);
	});

	it('forgets alerts dismissed for everyone until the next list', () => {
		const n = new Notices(() => 1, null);
		n.setAlerts([alert('a1'), alert('a2')]);
		n.forgetAlerts(['a1']);
		expect(n.list.map((x) => x.key)).toEqual(['alert:a2']);
		// Not a local dismissal: the alert shows again if it comes back.
		n.setAlerts([alert('a1'), alert('a2')]);
		expect(n.count).toBe(2);
	});

	it('keeps at most the newest dismissed keys and drops anything unexpected', () => {
		const store = new MemStorage();
		const n = new Notices(() => 1, store);
		for (let i = 0; i < MAX_DISMISSED + 5; i++) n.dismiss(`job:${i}`);
		const kept = JSON.parse(store.m.get(DISMISSED_KEY) ?? '[]') as string[];
		expect(kept).toHaveLength(MAX_DISMISSED);
		expect(kept[0]).toBe('job:5');
		n.dismiss('not a key', '<script>');
		expect(JSON.parse(store.m.get(DISMISSED_KEY) ?? '[]')).toHaveLength(MAX_DISMISSED);
		expect(parseDismissed('{broken')).toEqual([]);
		expect(parseDismissed('{"a":1}')).toEqual([]);
		expect(parseDismissed(JSON.stringify(['job:1', 7, 'x', 'alert:a:critical']))).toEqual([
			'job:1',
			'alert:a:critical'
		]);
	});

	it('announces the user’s own manual jobs when they finish, never old ones', () => {
		const n = new Notices(() => 1, null);
		const feed = jobNotices(
			() => 'me',
			(k) => (k === 'stack.deploy' ? 'Deploy stack' : k),
			n
		);
		const j = (
			id: string,
			state: string,
			by: string,
			at = '2026-09-25T12:00:00Z',
			origin = 'manual'
		) => ({
			id,
			kind: 'stack.deploy',
			state,
			createdAt: at,
			initiatorUserId: by,
			origin,
			targets: [{ type: 'stack', id: 'silo' }],
			error: { recovery: 'Fix the file and deploy again.' }
		});
		// First list: already finished jobs are history, not news.
		feed([
			j('1', 'failed', 'me'),
			j('2', 'running', 'me'),
			j('3', 'running', 'other'),
			j('4', 'running', 'other'),
			j('6', 'running', 'me', '2026-09-25T12:00:00Z', 'scheduled')
		]);
		expect(n.items).toHaveLength(0);
		feed([
			j('5', 'failed', 'me', '2026-09-25T12:01:00Z'), // new and already done (fast)
			j('1', 'failed', 'me'),
			j('2', 'succeeded', 'me'),
			j('3', 'succeeded', 'other'), // someone else's job: not the user's news
			j('4', 'partial', 'other'), // someone else's failure: theirs (or an alert)
			j('6', 'failed', 'me', '2026-09-25T12:00:00Z', 'scheduled') // an alert covers it
		]);
		expect(n.items.map((x) => [x.key, x.tone, x.title])).toEqual([
			['job:2', 'ok', 'Deploy stack silo succeeded'],
			['job:5', 'danger', 'Deploy stack silo failed']
		]);
		expect(n.items[1].body).toBe('Fix the file and deploy again.');
		expect(n.items[1].href).toBe('/jobs/5');
		// Refreshing the same list announces nothing new.
		feed([j('5', 'failed', 'me', '2026-09-25T12:01:00Z')]);
		expect(n.items).toHaveLength(2);
	});

	it('never shows an opaque target ID in a job notice', () => {
		const n = new Notices(() => 1, null);
		const feed = jobNotices(
			() => 'me',
			() => 'Deploy stack',
			n
		);
		const job = (state: string) => ({
			id: 'j1',
			kind: 'stack.deploy',
			state,
			createdAt: '2026-09-25T12:00:00Z',
			initiatorUserId: 'me',
			origin: 'manual',
			targets: [{ type: 'stack', id: '0190a6e0-0000-7000-8000-000000000001' }]
		});
		feed([job('running')]);
		feed([job('succeeded')]);
		expect(n.items.map((x) => x.title)).toEqual(['Deploy stack succeeded']);
	});

	it('links every notice somewhere', () => {
		expect(noticeHref({ kind: 'job', href: '/jobs/j1' })).toBe('/jobs/j1');
		expect(noticeHref({ kind: 'job' })).toBe('/jobs');
		expect(noticeHref({ kind: 'alert' })).toBe('/notifications?tab=alerts');
	});

	it('names generated update policies by what they update', () => {
		const id = '01a0e473-0000-7000-8000-000000000001';
		const auto = (pid: string, target: { type: string; id: string }) => ({
			id: pid,
			name: `Automatic update ${pid}`,
			environmentId: 'e1',
			target
		});
		expect(isGeneratedPolicyName({ id, name: `Automatic update ${id}` })).toBe(true);
		expect(isGeneratedPolicyName({ id, name: 'Silo images' })).toBe(false);
		const names = (t: { type: string; id: string }) => (t.id === 's1' ? 'zerobyte' : undefined);
		expect(policyLabel(auto(id, { type: 'stack', id: 's1' }), names)).toBe('zerobyte');
		expect(policyLabel(auto(id, { type: 'container', id: 'nginx' }))).toBe('container nginx');
		expect(policyLabel(auto(id, { type: 'stack', id: 's9' }), names)).toBe('a stack');
		// The manager names the target: its records read "Automatic updates
		// for zerobyte", the label names the stack itself.
		expect(
			policyLabel({
				id,
				name: 'Automatic updates for Zerobyte backups',
				target: { type: 'stack', id: 's1' },
				targetName: 'Zerobyte backups'
			})
		).toBe('Zerobyte backups');
		expect(policyLabel({ id, name: 'Silo images', target: { type: 'stack', id: 's1' } })).toBe(
			'Silo images'
		);
	});
});

describe('recent pages (palette, UI state only)', () => {
	it('keeps the newest paths per tab and never stores titles', () => {
		const store = new MemStorage();
		const recent = new RecentPages(store, 3);
		recent.visit('/stacks');
		recent.visit('/stacks/s1');
		recent.name('/stacks/s1', 'Silo');
		recent.visit('/jobs');
		recent.visit('/stacks');
		recent.visit('/settings');
		expect(recent.items.map((i) => i.path)).toEqual(['/settings', '/stacks', '/jobs']);
		expect(store.m.get('docker-manager:recent-pages')).toBe(
			JSON.stringify(['/settings', '/stacks', '/jobs'])
		);
		recent.visit('/stacks/s1');
		recent.name('/stacks/s1', 'Silo');
		expect(recent.items[0]).toEqual({ path: '/stacks/s1', title: 'Silo' });
		expect(store.m.get('docker-manager:recent-pages')).not.toContain('Silo');
		// A new tab session reads the paths back, without titles.
		expect(new RecentPages(store, 3).items).toEqual([
			{ path: '/stacks/s1' },
			{ path: '/settings' },
			{ path: '/stacks' }
		]);
		recent.visit('not a path');
		expect(recent.items).toHaveLength(3);
		store.m.set('docker-manager:recent-pages', '{broken');
		expect(new RecentPages(store).items).toEqual([]);
	});
});

describe('command palette model', () => {
	it('lists recent pages by title or section, without the current page', () => {
		const pages = visibleNav(accessOf(perms({ owner: true })));
		const out = recentResults(
			[
				{ path: '/jobs', title: 'Jobs' },
				{ path: '/stacks/s1', title: 'Silo' },
				{ path: '/stacks/s2' },
				{ path: '/settings' }
			],
			pages,
			'/jobs'
		);
		expect(out.map((r) => [r.group, r.label, r.secondary, r.href])).toEqual([
			['Recent', 'Silo', 'Stacks', '/stacks/s1'],
			['Recent', 'Settings', undefined, '/settings']
		]);
		expect(recentResults([{ path: '/a', title: 'A' }], pages, undefined, 0)).toEqual([]);
	});

	it('offers only the actions the caller may start, in the selected environment', () => {
		expect(actionResults(undefined, null)).toEqual([]);
		const owner = actionResults(accessOf(perms({ owner: true })), 'e1');
		expect(owner.map((a) => [a.label, a.href])).toEqual([
			['Create stack', '/stacks?create=1&environment=e1'],
			['Create container', '/containers/new?environment=e1'],
			['Build image', '/builds/new?environment=e1'],
			['Add environment', '/environments/add']
		]);
		const builder = actionResults(accessOf(perms({ entries: [allow('image.build')] })), null);
		expect(builder.map((a) => a.label)).toEqual(['Build image']);
		const stacksOnly = actionResults(accessOf(perms({ owner: true })), null, 'stack');
		expect(stacksOnly.map((a) => a.label)).toEqual(['Create stack']);
	});

	const hit = (h: Partial<SearchHit>): SearchHit =>
		({ type: 'stack', id: 'x', name: 'x', ...h }) as SearchHit;

	it('links every hit type to its page', () => {
		expect(hrefForHit(hit({ type: 'environment', id: 'e1' }))).toBe('/environments/e1');
		expect(hrefForHit(hit({ type: 'stack', id: 's1' }))).toBe('/stacks/s1');
		expect(
			hrefForHit(hit({ type: 'service', id: 's1/silo-db', name: 'silo-db', stackId: 's1' }))
		).toBe('/stacks/s1?service=silo-db');
		// Containers and networks open by name (their #17/#23 identity).
		expect(
			hrefForHit(hit({ type: 'container', id: 'c0ffee', name: 'we b', environmentId: 'e1' }))
		).toBe('/containers/e1/we%20b');
		expect(hrefForHit(hit({ type: 'image', id: 'sha256:ab', environmentId: 'e1' }))).toBe(
			'/images/e1/sha256%3Aab'
		);
		expect(hrefForHit(hit({ type: 'volume', id: 'data', environmentId: 'e1' }))).toBe(
			'/volumes/e1/data'
		);
		expect(
			hrefForHit(hit({ type: 'network', id: 'n1', name: 'backend', environmentId: 'e1' }))
		).toBe('/networks/e1/backend');
	});

	it('offers Profile and Settings as pages to everyone, found by what they hold', () => {
		const restricted = palettePages(accessOf(perms({})));
		expect(restricted.map((p) => p.id)).toEqual(['dashboard', 'settings', 'profile']);
		expect(pageResults(restricted, 'profile').map((p) => [p.label, p.href])).toEqual([
			['Profile', '/profile']
		]);
		expect(pageResults(restricted, 'Passkeys').map((p) => p.label)).toEqual(['Profile']);
		expect(pageResults(restricted, 'password').map((p) => p.label)).toEqual(['Profile']);
		expect(pageResults(restricted, 'audit').map((p) => p.label)).toEqual(['Settings']);
		// Settings → Move to a new server, found by the words people use for it.
		expect(pageResults(restricted, 'migrate manager').map((p) => p.label)).toEqual([
			'Settings'
		]);
		expect(pageResults(restricted, 'new server').map((p) => p.label)).toEqual(['Settings']);
		// My tokens are in Profile, every user's tokens in Settings.
		expect(pageResults(restricted, 'api tokens').map((p) => p.label)).toEqual([
			'Settings',
			'Profile'
		]);
		// Recent names a Profile tab by its section.
		const recent = recentResults(
			[{ path: '/profile/tokens' }, { path: '/profile' }],
			restricted
		);
		expect(recent.map((r) => [r.label, r.href])).toEqual([['Profile', '/profile']]);
	});

	it('groups pages and hits in order', () => {
		const pages = pageResults(visibleNav(accessOf(perms({ owner: true }))), 'sta');
		expect(pages.map((p) => p.label)).toEqual(['Stacks']);
		const hits = hitResults({
			query: 'silo',
			gaps: [],
			items: [
				hit({
					type: 'stack',
					id: 's1',
					name: 'Silo',
					environmentName: 'homelab',
					status: 'deployed'
				}),
				hit({
					type: 'container',
					id: 'c1',
					name: 'silo-web-1',
					environmentId: 'e1',
					environmentName: 'homelab'
				})
			]
		});
		expect(hits[0]).toMatchObject({
			group: 'Stacks',
			label: 'Silo',
			secondary: 'homelab, deployed'
		});
		expect(grouped([...pages, ...hits]).map((g) => g.group)).toEqual([
			'Pages',
			'Stacks',
			'Containers'
		]);
	});
});

describe('live connection banner and indicator (#23 liveStatus)', () => {
	it('shows the offline banner only after 5 s down', () => {
		expect(bannerDelay('live', 0, 10_000)).toBeNull();
		expect(bannerDelay('idle', 0, 10_000)).toBeNull();
		expect(bannerDelay('unauthenticated', 0, 10_000)).toBeNull();
		expect(bannerDelay('reconnecting', 1_000, 1_000)).toBe(OFFLINE_BANNER_DELAY_MS);
		expect(bannerDelay('reconnecting', 1_000, 4_000)).toBe(2_000);
		expect(bannerDelay('polling', 1_000, 60_000)).toBe(0);
	});

	it('names too many open tabs when that is why the stream stopped', () => {
		expect(bannerText(false).title).toBe('Live updates are disconnected');
		expect(bannerText(true).title).toBe('Too many Docker Manager tabs are open');
		expect(bannerText(true).body).toContain('Close tabs');
	});

	it('says nothing while live or signed out', () => {
		expect(indicatorText('live')).toBe('');
		expect(indicatorText('idle')).toBe('');
		expect(indicatorText('stopped')).toBe('');
		expect(indicatorText('unauthenticated')).toBe('');
		expect(indicatorText('reconnecting')).toBe('Reconnecting…');
		expect(indicatorText('polling')).toBe('Live updates paused');
	});
});
