import { describe, expect, it } from 'vitest';
import type { MyPermissions } from '$lib/api/client';
import {
	auditActionLabel,
	canAnywhere,
	canInEnvironment,
	candidateStatus,
	compareRevisions,
	comparisonFor,
	defaultComparison,
	dependencyOrder,
	downtimeText,
	findingTitle,
	groupRevisions,
	importCandidates,
	nameError,
	openTarget,
	revisionLabel,
	revisionSource,
	runningOf,
	serviceImageId,
	serviceNetworks,
	serviceCounts,
	servicePorts,
	serviceUrl,
	serviceUsage,
	serviceVolumes,
	shortDigest,
	shortHash,
	spaceCheck,
	stackStatus,
	stackTitle,
	stackUsage,
	statusSummary,
	upSince,
	updateAvailable,
	volumeText
} from './model';
import type { ContainerMetrics, Stack, StackContainer, StackServiceStatus } from './queries';

const engine = (state: string, services: [string, number, number][]) => ({
	state: state as NonNullable<Stack['engine']>['state'],
	services: services.map(([service, containers, running]) => ({ service, containers, running }))
});

const ctr = (over: Partial<StackContainer>): StackContainer =>
	({ state: 'running', view: 'full', ...over }) as StackContainer;

const svc = (name: string, containers: StackContainer[]): StackServiceStatus =>
	({
		name,
		containers,
		build: false,
		dependsOn: [],
		drift: [],
		status: 'running'
	}) as StackServiceStatus;

describe('stack status and counts', () => {
	it('names the stack from its display metadata', () => {
		expect(stackTitle({ name: 'silo', displayName: 'Silo' })).toBe('Silo');
		expect(stackTitle({ name: 'silo', displayName: '  ' })).toBe('silo');
	});

	it('prefers what Docker Manager did for failed, down and undeployed stacks, else the Engine state', () => {
		expect(stackStatus({ status: 'failed', engine: engine('running', []) })).toBe('failed');
		expect(stackStatus({ status: 'down' })).toBe('down');
		expect(stackStatus({ status: 'undeployed' })).toBe('undeployed');
		expect(stackStatus({ status: 'deployed', engine: engine('partial', []) })).toBe('partial');
		expect(stackStatus({ status: 'deployed', engine: engine('unknown', []) })).toBe('deployed');
		expect(stackStatus({ status: 'stopped' })).toBe('stopped');
	});

	it('counts services and containers, including services only the Engine knows', () => {
		const s = {
			services: [{ name: 'web' }, { name: 'db' }] as Stack['services'],
			engine: engine('partial', [
				['web', 2, 2],
				['db', 1, 0],
				['stray', 1, 1]
			])
		};
		expect(serviceCounts(s)).toEqual({
			servicesRunning: 2,
			services: 3,
			containersRunning: 3,
			containers: 4
		});
		expect(statusSummary({ status: 'deployed', ...s })).toBe('1 of 3 services not running');
		expect(
			statusSummary({
				status: 'deployed',
				services: s.services,
				engine: engine('running', [
					['web', 1, 1],
					['db', 1, 1]
				])
			})
		).toBe('All services running');
		expect(statusSummary({ status: 'failed' })).toBe('The last deploy failed');
	});
});

describe('ports and links', () => {
	const containers = [
		ctr({
			ports: [
				{ privatePort: 80, publicPort: 8080, protocol: 'tcp' },
				{ privatePort: 443, protocol: 'tcp' }
			]
		}),
		ctr({
			ports: [
				{ privatePort: 80, publicPort: 8080, protocol: 'tcp' },
				{ privatePort: 53, publicPort: 53, protocol: 'udp' }
			]
		})
	];

	it('lists published ports once and links TCP ports only with a service address', () => {
		expect(servicePorts(containers)).toEqual([{ label: '8080:80' }, { label: '53:53/udp' }]);
		expect(servicePorts(containers, '192.168.1.10')).toEqual([
			{ label: '8080:80', href: 'http://192.168.1.10:8080' },
			{ label: '53:53/udp', href: undefined }
		]);
	});

	it('opens the first web port, and nothing without an address or a published TCP port', () => {
		expect(openTarget(containers, 'nas.lan')).toBe('http://nas.lan:8080');
		expect(openTarget(containers)).toBeUndefined();
		expect(
			openTarget([ctr({ ports: [{ privatePort: 80, protocol: 'tcp' }] })], 'nas.lan')
		).toBeUndefined();
	});

	it('brackets IPv6 addresses', () => {
		expect(serviceUrl('fd00::10', 8080)).toBe('http://[fd00::10]:8080');
		expect(serviceUrl('[fd00::10]', 8080)).toBe('http://[fd00::10]:8080');
	});

	it('counts running containers and finds the oldest start', () => {
		const s = [
			svc('web', [
				ctr({ startedAt: '2026-09-10T00:00:00Z' }),
				ctr({ state: 'exited', startedAt: '2026-01-01T00:00:00Z' })
			]),
			svc('db', [ctr({ startedAt: '2026-09-01T05:00:00Z' })])
		];
		expect(runningOf(s[0])).toEqual({ running: 1, total: 2 });
		expect(upSince(s)).toBe('2026-09-01T05:00:00Z');
		expect(upSince([svc('x', [ctr({ state: 'exited' })])])).toBeUndefined();
	});
});

describe('usage', () => {
	const metrics = (
		container: string,
		cpu: (number | null)[],
		mem: (number | null)[]
	): ContainerMetrics =>
		({
			container,
			timestamps: cpu.map((_, i) => `2026-09-25T10:0${i}:00Z`),
			series: [
				{ key: 'cpu.percent', unit: 'percent', values: cpu },
				{ key: 'memory.used_bytes', unit: 'bytes', values: mem }
			]
		}) as ContainerMetrics;

	it('sums CPU per timestamp, keeps gaps as null and takes the latest values', () => {
		const u = stackUsage([
			metrics('a', [1, null, 2], [100, 200, null]),
			metrics('b', [null, null, 3], [50, null, 70])
		]);
		expect(u.cpu).toEqual([1, null, 5]);
		expect(u.cpuNow).toBe(5);
		expect(u.memoryNow).toBe(270);
		expect(u.containers).toEqual({ a: { cpu: 2, memory: 200 }, b: { cpu: 3, memory: 70 } });
		expect(serviceUsage(svc('web', [ctr({ name: 'a' }), ctr({ name: 'b' })]), u)).toEqual({
			cpu: 5,
			memory: 270
		});
		expect(serviceUsage(svc('x', [ctr({ name: 'z' })]), u)).toEqual({
			cpu: null,
			memory: null
		});
	});

	it('has no values without samples (never zero)', () => {
		const u = stackUsage([]);
		expect(u.cpuNow).toBeNull();
		expect(u.memoryNow).toBeNull();
	});

	it('takes the current values from the newest samples when given', () => {
		const u = stackUsage(
			[metrics('a', [1, 2], [100, 200]), metrics('b', [3, 4], [50, 60])],
			[
				{ container: 'a', cpuPercent: 1.5, memoryUsedBytes: 120 },
				{ container: 'c', memoryUsedBytes: 30 }
			]
		);
		// The chart keeps the history; b has no recent sample (stopped).
		expect(u.cpu).toEqual([4, 6]);
		expect(u.cpuNow).toBe(1.5);
		expect(u.memoryNow).toBe(150);
		expect(u.containers).toEqual({
			a: { cpu: 1.5, memory: 120 },
			c: { cpu: null, memory: 30 }
		});
		expect(stackUsage([], []).cpuNow).toBeNull();
	});
});

describe('service networks', () => {
	it('collects the networks of the containers once, with their addresses, IPv4 first', () => {
		const s = svc('web', [
			ctr({ name: 'a', networks: [{ name: 'shop_default', ipAddress: '172.18.0.2' }] }),
			ctr({
				name: 'b',
				networks: [
					{ name: 'shop_default', ipAddress: '172.18.0.3', ipv6Address: 'fd00::3' }
				]
			}),
			ctr({ name: 'c', state: 'exited', networks: [{ name: 'shop_default' }] })
		]);
		expect(serviceNetworks(s)).toEqual([
			{ name: 'shop_default', addresses: ['172.18.0.2', '172.18.0.3', 'fd00::3'] }
		]);
	});
});

describe('service volumes and image', () => {
	const anon = 'ab12'.repeat(16);

	it('lists each volume once, named before anonymous ones, with its mount paths', () => {
		const s = svc('web', [
			ctr({
				name: 'a',
				volumes: [
					{ name: anon, destination: '/cache', anonymous: true },
					{ name: 'shop_data', destination: '/data', readOnly: true }
				]
			}),
			ctr({
				name: 'b',
				volumes: [
					{ name: 'shop_data', destination: '/data' },
					{ name: 'shop_conf', destination: '/etc/app', readOnly: true }
				]
			}),
			ctr({ name: 'c' })
		]);
		const vols = serviceVolumes(s);
		expect(vols).toEqual([
			{ name: 'shop_data', anonymous: false, destinations: ['/data'], readOnly: false },
			{ name: 'shop_conf', anonymous: false, destinations: ['/etc/app'], readOnly: true },
			{ name: anon, anonymous: true, destinations: ['/cache'], readOnly: false }
		]);
		expect(vols.map(volumeText)).toEqual([
			'shop_data at /data',
			'shop_conf at /etc/app (read-only)',
			`Anonymous volume ${anon} at /cache`
		]);
		expect(serviceVolumes(svc('db', [ctr({ view: 'minimal' })]))).toEqual([]);
	});

	it('links the image a container runs, else the one the last deploy applied', () => {
		const applied = { service: 'web', image: 'nginx:1', imageId: 'sha256:old', build: false };
		const s = svc('web', [
			ctr({ name: 'a', state: 'exited', imageId: 'sha256:stopped' }),
			ctr({ name: 'b', imageId: 'sha256:run' })
		]);
		expect(serviceImageId({ ...s, applied })).toBe('sha256:run');
		expect(
			serviceImageId(svc('web', [ctr({ state: 'exited', imageId: 'sha256:stopped' })]))
		).toBe('sha256:stopped');
		// Minimal containers carry no image ID: the applied one.
		expect(serviceImageId({ ...svc('web', [ctr({ view: 'minimal' })]), applied })).toBe(
			'sha256:old'
		);
		expect(serviceImageId(svc('web', []))).toBeUndefined();
	});
});

describe('revisions', () => {
	const f = (path: string, content: string, sha = content) => ({
		path,
		content,
		sha256: sha,
		size: content.length,
		encoding: 'utf-8' as const
	});

	it('labels revisions and their sources', () => {
		expect(shortHash('sha256:0c712efabc')).toBe('0c712ef');
		expect(revisionLabel({ seq: 3, hash: '0c712efabc' })).toBe('Revision 3 (0c712ef)');
		expect(revisionSource('file_manager')).toBe('File Manager');
		expect(revisionSource('external')).toBe('Edited on Disk');
	});

	// Newest first: 5 and 4 have the same files, as have 2 and 1.
	const revs = [
		{ id: 'r5', hash: 'c' },
		{ id: 'r4', hash: 'c' },
		{ id: 'r3', hash: 'b' },
		{ id: 'r2', hash: 'a' },
		{ id: 'r1', hash: 'a' }
	];

	it('groups consecutive revisions with the same fingerprint', () => {
		expect(groupRevisions(revs).map((g) => [g.head.id, g.members.map((m) => m.id)])).toEqual([
			['r5', ['r5', 'r4']],
			['r3', ['r3']],
			['r2', ['r2', 'r1']]
		]);
		expect(groupRevisions([])).toEqual([]);
	});

	it('never opens a comparison of two revisions with the same files', () => {
		// Undeployed changes: deployed against on disk.
		expect(defaultComparison(revs, 'r3', 'r5', true)).toEqual({ from: 'r3', to: 'r5' });
		// Otherwise the newest run against the one before it (not r5 vs r4).
		expect(defaultComparison(revs, 'r5', 'r5', false)).toEqual({ from: 'r3', to: 'r5' });
		// "Undeployed" but equal files: fall back to the runs.
		expect(defaultComparison(revs, 'r4', 'r5', true)).toEqual({ from: 'r3', to: 'r5' });
		expect(defaultComparison(revs.slice(0, 2), 'r5', 'r5', false)).toBeNull();
	});

	it('compares a revision with the deployed one, else the next older with other files', () => {
		expect(comparisonFor(revs, revs[2], 'r5')).toEqual({ from: 'r5', to: 'r3' });
		expect(comparisonFor(revs, revs[0], 'r4')).toEqual({ from: 'r3', to: 'r5' });
		expect(comparisonFor(revs, revs[3], 'r1')).toBeNull();
	});

	it('compares the files of two revisions in path order', () => {
		const a = { files: [f('compose.yaml', 'a\n'), f('.env', 'X=1\n'), f('old.yaml', 'o\n')] };
		const b = {
			files: [
				f('compose.yaml', 'b\n'),
				f('.env', 'X=1\n'),
				f('compose.override.yaml', 'n\n'),
				{
					path: 'logo.png',
					sha256: 'p',
					size: 3,
					content: 'AAA',
					encoding: 'base64' as const
				}
			]
		};
		expect(compareRevisions(a, b).map((c) => [c.path, c.status])).toEqual([
			['.env', 'same'],
			['compose.override.yaml', 'added'],
			['compose.yaml', 'changed'],
			['logo.png', 'binary'],
			['old.yaml', 'removed']
		]);
	});
});

describe('forms and permissions', () => {
	it('validates Compose project names', () => {
		expect(nameError('silo')).toBeUndefined();
		expect(nameError('my_stack-2')).toBeUndefined();
		expect(nameError('')).toBe('Enter a name.');
		expect(nameError('Silo')).toMatch(/lower-case/);
		expect(nameError('-silo')).toMatch(/starting with/);
		expect(nameError('a'.repeat(64))).toMatch(/63/);
	});

	const perms = (
		entries: { capability: string; allowed: boolean; kind: string; env?: string }[],
		owner = false
	) =>
		({
			owner,
			catalogVersion: 1,
			environments: [],
			entries: entries.map((e) => ({
				capability: e.capability,
				allowed: e.allowed,
				reason: '',
				source: 'group_rule',
				scope: { kind: e.kind, environmentId: e.env }
			}))
		}) as unknown as MyPermissions;

	it('decides environment capabilities with the environment rule before the instance rule', () => {
		const p = perms([
			{ capability: 'stack.create', allowed: true, kind: 'instance' },
			{ capability: 'stack.create', allowed: false, kind: 'environment', env: 'edge' }
		]);
		expect(canInEnvironment(p, 'stack.create', 'homelab')).toBe(true);
		expect(canInEnvironment(p, 'stack.create', 'edge')).toBe(false);
		expect(canInEnvironment(p, 'stack.import', 'homelab')).toBe(false);
		expect(canInEnvironment(perms([], true), 'stack.import', 'x')).toBe(true);
		expect(canInEnvironment(undefined, 'stack.create', 'x')).toBe(false);
		expect(canAnywhere(p, 'stack.create')).toBe(true);
		expect(canAnywhere(p, 'audit.read')).toBe(false);
	});
});

describe('migration, updates and jobs', () => {
	it('orders services dependencies first and survives cycles', () => {
		expect(
			dependencyOrder([
				{ name: 'web', dependsOn: [{ service: 'api' }] },
				{ name: 'api', dependsOn: [{ service: 'db' }, { service: 'cache' }] },
				{ name: 'db' },
				{ name: 'cache' }
			])
		).toEqual(['db', 'cache', 'api', 'web']);
		expect(
			dependencyOrder([
				{ name: 'a', dependsOn: [{ service: 'b' }] },
				{ name: 'b', dependsOn: [{ service: 'a' }] }
			])
		).toEqual(['b', 'a']);
	});

	it('compares data size with the free space on the destination', () => {
		const d = {
			projectBytes: 10,
			volumeBytes: 90,
			imageBytes: 0,
			totalBytes: 100,
			destinationStacksFree: 1000,
			destinationVolumesFree: 100
		};
		expect(spaceCheck(d)).toBe('ok');
		expect(spaceCheck({ ...d, destinationVolumesFree: 99 })).toBe('short');
		expect(spaceCheck({ ...d, destinationStacksFree: -1 })).toBe('unknown');
	});

	it('says downtime and findings in words', () => {
		expect(downtimeText(0)).toBe('No downtime expected');
		expect(downtimeText(40)).toBe('About 40 s');
		expect(downtimeText(185)).toBe('About 3 min');
		expect(downtimeText(3 * 3600)).toBe('About 3 h');
		expect(findingTitle('port_conflict')).toBe('Port Already in Use');
		expect(findingTitle('network_not_creatable')).toBe('Network Must Be Created by Hand');
		expect(findingTitle('archive_too_large')).toBe('Archive Too Large');
		expect(findingTitle('project_name_pinned')).toBe('Project Name Pinned');
		expect(findingTitle('some_new_code')).toBe('Some new code');
	});

	it('shortens digests and names update states', () => {
		expect(shortDigest('redis@sha256:91b0a4c2d3e4f5a6b7')).toBe('91b0a4c2d3e4');
		expect(shortDigest('sha256:858f009f9709ce57aa')).toBe('858f009f9709');
		expect(shortDigest(undefined)).toBe('—');
		expect(candidateStatus('update_available')).toBe('Update Available');
		expect(
			updateAvailable([{ update: 'up_to_date' }, { update: 'update_available' }] as never)
		).toBe(true);
		expect(updateAvailable([])).toBe(false);
	});

	it('names job kinds and audit actions', () => {
		expect(auditActionLabel('stack.deploy')).toBe('Deploy Stack');
		expect(auditActionLabel('stack.definition.read')).toBe('Opened the Definition');
		expect(auditActionLabel('stack.validate')).toBe('Validated the Definition');
		expect(auditActionLabel('stack.restart')).toBe('Restart Stack');
		expect(auditActionLabel('stack.export.preview')).toBe('Check Stack Export');
		expect(auditActionLabel('stack.export')).toBe('Export Archive');
		expect(auditActionLabel('stack.files.write')).toBe('Stack files write');
	});
});

describe('importCandidates', () => {
	const projects = [
		{ name: 'zerobyte' },
		{ name: 'docker-manager', stackId: 's1' },
		{ name: 'garage' }
	];

	it('hides managed projects', () => {
		expect(importCandidates(projects, true).map((p) => p.name)).toEqual(['garage', 'zerobyte']);
	});

	it('keeps a managed project whose import the dialog started', () => {
		const keep = (p: { name: string }) => p.name === 'docker-manager';
		expect(importCandidates(projects, true, keep).map((p) => p.name)).toEqual([
			'garage',
			'zerobyte',
			'docker-manager'
		]);
	});

	it('lists managed projects last when shown', () => {
		expect(importCandidates(projects, false).map((p) => p.name)).toEqual([
			'garage',
			'zerobyte',
			'docker-manager'
		]);
	});
});
