import { describe, expect, it } from 'vitest';
import type { MyPermissions, SearchHit } from '$lib/api/client';
import { EnvironmentSelection, type StorageLike } from './environment.svelte';
import { accessOf, activeNav, isRestricted, NAV_GROUPS, NAV_ITEMS, visibleNav } from './nav';
import {
	environmentNotices,
	isGeneratedPolicyName,
	jobNotices,
	noticeHref,
	Notices,
	policyLabel,
	updateNotices
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
import { bannerDelay, indicatorText, OFFLINE_BANNER_DELAY_MS } from './live-banner';

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
		expect(ids).toHaveLength(17);
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
		expect(activeNav('/settings/tokens')?.id).toBe('settings');
		expect(activeNav('/nowhere')).toBeUndefined();
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
	it('deduplicates by key, counts unread and marks read', () => {
		let t = 0;
		const n = new Notices(() => ++t);
		n.push({ key: 'job:1', kind: 'job', tone: 'ok', title: 'Deployed Silo' });
		n.push({ key: 'job:2', kind: 'job', tone: 'danger', title: 'Deploy of Media failed' });
		n.push({ key: 'job:1', kind: 'job', tone: 'ok', title: 'Deployed Silo again' });
		expect(n.items.map((x) => x.title)).toEqual([
			'Deployed Silo again',
			'Deploy of Media failed'
		]);
		expect(n.unread).toBe(2);
		n.markAllRead();
		expect(n.unread).toBe(0);
		n.resolve('job:2');
		expect(n.items).toHaveLength(1);
	});

	it('turns environment transitions into offline notices and resolves them', () => {
		const n = new Notices(() => 1);
		const feed = environmentNotices(n);
		feed([
			{ id: 'e1', name: 'homelab', online: true },
			{ id: 'e2', name: 'edge', online: false }
		]);
		expect(n.items.map((x) => x.title)).toEqual(['edge is offline']);
		feed([
			{ id: 'e1', name: 'homelab', online: false },
			{ id: 'e2', name: 'edge', online: false }
		]);
		expect(n.items.map((x) => x.title)).toEqual(['homelab is offline', 'edge is offline']);
		feed([
			{ id: 'e1', name: 'homelab', online: true },
			{ id: 'e2', name: 'edge', online: false }
		]);
		expect(n.items.map((x) => x.title)).toEqual(['edge is offline']);
		expect(n.items[0].href).toBe('/environments/e2');
	});

	it('announces finished jobs: the user’s own always, anyone’s failures, never old ones', () => {
		const n = new Notices(() => 1);
		const feed = jobNotices(
			() => 'me',
			(k) => (k === 'stack.deploy' ? 'Deploy stack' : k),
			n
		);
		const j = (id: string, state: string, by: string, at = '2026-09-25T12:00:00Z') => ({
			id,
			kind: 'stack.deploy',
			state,
			createdAt: at,
			initiatorUserId: by,
			targets: [{ type: 'stack', id: 'silo' }],
			error: { recovery: 'Fix the file and deploy again.' }
		});
		// First list: already finished jobs are history, not news.
		feed([
			j('1', 'failed', 'me'),
			j('2', 'running', 'me'),
			j('3', 'running', 'other'),
			j('4', 'running', 'other')
		]);
		expect(n.items).toHaveLength(0);
		feed([
			j('5', 'succeeded', 'me', '2026-09-25T12:01:00Z'), // new and already done (fast)
			j('1', 'failed', 'me'),
			j('2', 'succeeded', 'me'),
			j('3', 'succeeded', 'other'), // someone else's success: not news
			j('4', 'partial', 'other') // someone else's (or a schedule's) failure: news
		]);
		expect(n.items.map((x) => [x.key, x.tone, x.title])).toEqual([
			['job:4', 'warn', 'Deploy stack silo partly failed'],
			['job:2', 'ok', 'Deploy stack silo succeeded'],
			['job:5', 'ok', 'Deploy stack silo succeeded']
		]);
		expect(n.items[0].body).toBe('Fix the file and deploy again.');
		expect(n.items[0].href).toBe('/jobs/4');
		// Refreshing the same list announces nothing new.
		feed([j('4', 'partial', 'other')]);
		expect(n.items).toHaveLength(3);
	});

	it('never shows an opaque target ID in a job notice', () => {
		const n = new Notices(() => 1);
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
			targets: [{ type: 'stack', id: '0190a6e0-0000-7000-8000-000000000001' }]
		});
		feed([job('running')]);
		feed([job('succeeded')]);
		expect(n.items.map((x) => x.title)).toEqual(['Deploy stack succeeded']);
	});

	it('links every notice somewhere', () => {
		expect(noticeHref({ kind: 'job', href: '/jobs/j1' })).toBe('/jobs/j1');
		expect(noticeHref({ kind: 'job' })).toBe('/jobs');
		expect(noticeHref({ kind: 'environment' })).toBe('/environments');
		expect(noticeHref({ kind: 'update' })).toBe('/updates');
	});

	it('names generated update policies by what they update and collapses several', () => {
		const id = '01a0e473-0000-7000-8000-000000000001';
		const auto = (pid: string, target: { type: string; id: string }, available: number) => ({
			id: pid,
			name: `Automatic update ${pid}`,
			environmentId: 'e1',
			target,
			summary: { available }
		});
		expect(isGeneratedPolicyName({ id, name: `Automatic update ${id}` })).toBe(true);
		expect(isGeneratedPolicyName({ id, name: 'Silo images' })).toBe(false);
		const names = (t: { type: string; id: string }) => (t.id === 's1' ? 'zerobyte' : undefined);
		expect(policyLabel(auto(id, { type: 'stack', id: 's1' }, 1), names)).toBe('zerobyte');
		expect(policyLabel(auto(id, { type: 'container', id: 'nginx' }, 1))).toBe(
			'container nginx'
		);
		expect(policyLabel(auto(id, { type: 'stack', id: 's9' }, 1), names)).toBe('a stack');

		const n = new Notices(() => 1);
		const feed = updateNotices(n, names);
		feed([auto(id, { type: 'stack', id: 's1' }, 2)]);
		expect(n.items.map((x) => x.title)).toEqual(['2 updates available for zerobyte']);
		expect(n.items[0].title).not.toContain(id);

		const many = Array.from({ length: 6 }, (_, i) =>
			auto(`p${i}`, { type: 'stack', id: `s${i + 1}` }, 1)
		);
		feed(many);
		expect(n.items).toHaveLength(1);
		expect(n.items[0]).toMatchObject({
			key: 'update:summary',
			title: '6 stacks have updates available',
			body: 'zerobyte, a stack, a stack and 3 more.',
			href: '/updates'
		});
		feed([...many.slice(0, 2), auto('c1', { type: 'container', id: 'nginx' }, 3)]);
		expect(n.items.map((x) => x.title)).toEqual([
			'2 stacks and 1 container have updates available'
		]);
		// The same target through two policies counts once.
		feed([
			auto('p0', { type: 'stack', id: 's1' }, 1),
			auto('p9', { type: 'stack', id: 's1' }, 1)
		]);
		expect(n.items.map((x) => [x.key, x.title])).toEqual([
			['update:p0', '1 update available for zerobyte']
		]);
		feed([]);
		expect(n.items).toHaveLength(0);
	});

	it('shows available updates per policy and resolves them when applied', () => {
		const n = new Notices(() => 1);
		const feed = updateNotices(n);
		feed([
			{ id: 'p1', name: 'Silo images', summary: { available: 2 } },
			{ id: 'p2', name: 'Media', summary: { available: 0 } }
		]);
		expect(n.items.map((x) => x.title)).toEqual(['2 updates available for Silo images']);
		expect(n.items[0].href).toBe('/updates/p1');
		n.markAllRead();
		feed([{ id: 'p1', name: 'Silo images', summary: { available: 2 } }]);
		expect(n.unread).toBe(0); // unchanged: not pushed again
		feed([{ id: 'p1', name: 'Silo images', summary: { available: 1 } }]);
		expect(n.items[0].title).toBe('1 update available for Silo images');
		feed([{ id: 'p1', name: 'Silo images', summary: { available: 0 } }]);
		expect(n.items).toHaveLength(0);
		feed([{ id: 'p3', name: 'X', summary: { available: 1 } }]);
		feed([]);
		expect(n.items).toHaveLength(0);
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

	it('says nothing while live or signed out', () => {
		expect(indicatorText('live')).toBe('');
		expect(indicatorText('idle')).toBe('');
		expect(indicatorText('stopped')).toBe('');
		expect(indicatorText('unauthenticated')).toBe('');
		expect(indicatorText('reconnecting')).toBe('Reconnecting…');
		expect(indicatorText('polling')).toBe('Live updates paused');
	});
});
