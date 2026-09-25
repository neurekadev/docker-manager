import { describe, expect, it } from 'vitest';
import type { MyPermissions, SearchHit } from '$lib/api/client';
import { EnvironmentSelection, type StorageLike } from './environment.svelte';
import { accessOf, activeNav, isRestricted, visibleNav } from './nav';
import { environmentNotices, jobNotices, Notices, updateNotices } from './notices.svelte';
import { grouped, hitResults, hrefForHit, pageResults } from './palette';
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
		expect(ids).toContain('schedules');
		expect(ids).toHaveLength(16);
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

	it('finds the active section, including environment-scoped details', () => {
		expect(activeNav('/')?.id).toBe('dashboard');
		expect(activeNav('/stacks/abc/files')?.id).toBe('stacks');
		expect(activeNav('/environments/e1')?.id).toBe('environments');
		expect(activeNav('/environments/e1/containers/c9')?.id).toBe('containers');
		expect(activeNav('/environments/e1/volumes/data')?.id).toBe('volumes');
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
		expect(store.m.get('dockyard:environment:u1')).toBe('e2');

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
		expect(store.m.has('dockyard:environment:u1')).toBe(false);
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

	it('shows available updates per policy and resolves them when applied', () => {
		const n = new Notices(() => 1);
		const feed = updateNotices(n);
		feed([
			{ id: 'p1', name: 'Silo images', summary: { available: 2 } },
			{ id: 'p2', name: 'Media', summary: { available: 0 } }
		]);
		expect(n.items.map((x) => x.title)).toEqual(['2 updates available for Silo images']);
		expect(n.items[0].href).toBe('/updates');
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

describe('command palette model', () => {
	const hit = (h: Partial<SearchHit>): SearchHit =>
		({ type: 'stack', id: 'x', name: 'x', ...h }) as SearchHit;

	it('links every hit type to its page', () => {
		expect(hrefForHit(hit({ type: 'environment', id: 'e1' }))).toBe('/environments/e1');
		expect(hrefForHit(hit({ type: 'stack', id: 's1' }))).toBe('/stacks/s1');
		expect(
			hrefForHit(hit({ type: 'service', id: 's1/silo-db', name: 'silo-db', stackId: 's1' }))
		).toBe('/stacks/s1?service=silo-db');
		expect(hrefForHit(hit({ type: 'container', id: 'c/1', environmentId: 'e1' }))).toBe(
			'/environments/e1/containers/c%2F1'
		);
		expect(hrefForHit(hit({ type: 'image', id: 'sha256:ab', environmentId: 'e1' }))).toBe(
			'/environments/e1/images/sha256%3Aab'
		);
		expect(hrefForHit(hit({ type: 'volume', id: 'data', environmentId: 'e1' }))).toBe(
			'/environments/e1/volumes/data'
		);
		expect(hrefForHit(hit({ type: 'network', id: 'n1', environmentId: 'e1' }))).toBe(
			'/environments/e1/networks/n1'
		);
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
