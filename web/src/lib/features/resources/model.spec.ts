import { describe, expect, it } from 'vitest';
import {
	attachedContainers,
	compactDuration,
	containerStatus,
	envLines,
	healthCommand,
	healthLabel,
	hostnameIsId,
	imagePresent,
	isSystemLabel,
	joinCommand,
	megabytes,
	megabytesField,
	needsAddressLookup,
	networkAliases,
	networkEntries,
	normalizeReference,
	parsePairs,
	portHref,
	portText,
	restartPolicyLabel,
	RESTART_OPTIONS,
	sameEverywhere,
	shortDigest,
	splitCommand,
	splitLabels,
	splitUntagged,
	uniquePorts,
	upSince,
	uptimeSortValue,
	volumeAccess
} from './model';

describe('volume access (#28: local volumes only)', () => {
	it('opens local volumes on the host', () => {
		expect(volumeAccess({ driver: 'local', options: {} })).toEqual({ local: true });
		expect(volumeAccess({})).toEqual({ local: true });
	});

	it('lists plugin drivers and remote-backed local volumes read-only, with the reason', () => {
		const plugin = volumeAccess({ driver: 'rclone' });
		expect(plugin.local).toBe(false);
		expect(plugin.reason).toContain('"rclone"');
		const nfs = volumeAccess({ driver: 'local', options: { type: 'nfs', o: 'addr=10.0.0.2' } });
		expect(nfs.local).toBe(false);
		expect(nfs.reason).toContain('nfs');
		expect(volumeAccess({ driver: 'local', options: { o: 'addr=10.0.0.2,rw' } }).local).toBe(
			false
		);
		expect(volumeAccess({ driver: 'local', options: { type: 'CIFS' } }).local).toBe(false);
		expect(volumeAccess({ driver: 'local', options: { type: 'tmpfs' } }).local).toBe(true);
	});
});

describe('ports', () => {
	it('renders published and exposed ports like docker ps', () => {
		expect(portText({ containerPort: 80, hostPort: 8080, protocol: 'tcp' })).toBe('8080:80');
		expect(portText({ containerPort: 53, hostPort: 53, protocol: 'udp' })).toBe('53:53/udp');
		expect(
			portText({ containerPort: 8080, hostPort: 8080, hostIp: '127.0.0.1', protocol: 'tcp' })
		).toBe('127.0.0.1:8080:8080');
		expect(portText({ containerPort: 9000, protocol: 'tcp' })).toBe('9000/tcp');
		expect(
			portText({ containerPort: 80, hostPort: 80, hostIp: '0.0.0.0', protocol: 'tcp' })
		).toBe('80:80');
	});

	it('drops the IPv4/IPv6 duplicates and sorts', () => {
		const ports = uniquePorts([
			{ containerPort: 80, hostPort: 8080, hostIp: '::', protocol: 'tcp' },
			{ containerPort: 53, hostPort: 53, protocol: 'udp' },
			{ containerPort: 80, hostPort: 8080, hostIp: '0.0.0.0', protocol: 'tcp' }
		]);
		expect(ports.map(portText)).toEqual(['53:53/udp', '8080:80']);
		expect(uniquePorts(undefined)).toEqual([]);
	});

	it('links web ports only with a service address and a public binding', () => {
		const p = { containerPort: 80, hostPort: 8080, protocol: 'tcp' as const };
		expect(portHref(p, '192.168.1.10')).toBe('http://192.168.1.10:8080');
		expect(portHref({ ...p, containerPort: 443, hostPort: 8443 }, 'h')).toBe('https://h:8443');
		expect(portHref(p, undefined)).toBeUndefined();
		expect(portHref({ ...p, protocol: 'udp' }, 'h')).toBeUndefined();
		expect(portHref({ ...p, hostIp: '127.0.0.1' }, 'h')).toBeUndefined();
		expect(portHref({ containerPort: 80, protocol: 'tcp' }, 'h')).toBeUndefined();
	});
});

describe('form parsers', () => {
	it('parses key=value lines and reports bad lines', () => {
		expect(parsePairs('a=1\n\n# comment\nb = two=2\nbad\n=x')).toEqual({
			values: { a: '1', b: ' two=2' },
			invalid: [5, 6]
		});
	});

	it('keeps environment lines verbatim (order, duplicates, spaces in values)', () => {
		expect(envLines('TZ=UTC\nMSG=hello world\nTZ=Europe/Berlin\nnope')).toEqual({
			values: ['TZ=UTC', 'MSG=hello world', 'TZ=Europe/Berlin'],
			invalid: [4]
		});
	});

	it('splits command lines with quotes and escapes, and reports open quotes', () => {
		expect(splitCommand(`nginx -g "daemon off;"`)).toEqual(['nginx', '-g', 'daemon off;']);
		expect(splitCommand(`sh -c 'echo "hi there"'`)).toEqual(['sh', '-c', 'echo "hi there"']);
		expect(splitCommand(`a\\ b "" c`)).toEqual(['a b', '', 'c']);
		expect(splitCommand('  ')).toEqual([]);
		expect(splitCommand('echo "open')).toBeNull();
		expect(joinCommand(['sh', '-c', 'echo hi'])).toBe('sh -c "echo hi"');
		expect(joinCommand(undefined)).toBe('');
	});

	it('converts megabytes and durations', () => {
		expect(megabytes('')).toBeUndefined();
		expect(megabytes('512')).toBe(512 * 1024 * 1024);
		expect(megabytes('x')).toBeNaN();
		// The field keeps full precision, so an unedited limit saves unchanged.
		for (const bytes of [512 * 1024 * 1024, 536_870_913, 1_234_567_891])
			expect(megabytes(megabytesField(bytes))).toBe(bytes);
		expect(megabytesField(536_870_913)).not.toBe('512');
		expect(megabytesField(undefined)).toBe('');
		expect(compactDuration(48_000)).toBe('48s');
		expect(compactDuration(192_000)).toBe('3m 12s');
		expect(compactDuration(3_900_000)).toBe('1h 5m');
		expect(compactDuration(undefined)).toBe('—');
		expect(shortDigest('sha256:4e1b5f1a6d8e0000')).toBe('4e1b5f1a6d8e');
	});
});

describe('image presence (#25: creating a container never pulls)', () => {
	const images = [
		{ id: 'sha256:aaaabbbbccccdddd', repoTags: ['nginx:1.27', 'ghcr.io/acme/app:1.4'] },
		{ id: 'sha256:1111222233334444', repoTags: ['redis:latest'] }
	];

	it('normalizes Docker Hub names and the implicit latest tag', () => {
		expect(normalizeReference('docker.io/library/nginx:1.27')).toBe('nginx:1.27');
		expect(normalizeReference('redis')).toBe('redis:latest');
		expect(normalizeReference('localhost:5000/app')).toBe('localhost:5000/app:latest');
		expect(normalizeReference('acme/app@sha256:ff')).toBe('acme/app@sha256:ff');
	});

	it('finds references and ID prefixes', () => {
		expect(imagePresent('nginx:1.27', images)).toBe(true);
		expect(imagePresent('docker.io/library/nginx:1.27', images)).toBe(true);
		expect(imagePresent('redis', images)).toBe(true);
		expect(imagePresent('ghcr.io/acme/app:1.4', images)).toBe(true);
		expect(imagePresent('nginx:1.28', images)).toBe(false);
		expect(imagePresent('aaaabbbbcccc', images)).toBe(true);
		expect(imagePresent('sha256:1111222233334444', images)).toBe(true);
		expect(imagePresent('', images)).toBe(false);
	});
});

describe('container status', () => {
	it('shows health while running', () => {
		expect(containerStatus({ state: 'running', health: 'unhealthy' })).toBe('unhealthy');
		expect(containerStatus({ state: 'running', health: 'healthy' })).toBe('running');
		expect(containerStatus({ state: 'exited', health: 'unhealthy' })).toBe('exited');
	});
});

describe('container addresses and uptime', () => {
	it('merges the networks of replicas, each once with all its addresses', () => {
		expect(
			networkEntries([
				[
					{ name: 'shop_default', ipv6Address: 'fd00::3', ipAddress: '172.18.0.3' },
					{ name: 'host' }
				],
				[{ name: 'shop_default', ipAddress: '172.18.0.4' }, { name: '' }],
				undefined
			])
		).toEqual([
			{ name: 'shop_default', addresses: ['172.18.0.3', '172.18.0.4', 'fd00::3'] },
			{ name: 'host', addresses: [] }
		]);
	});

	it('counts uptime only while a container is up', () => {
		const at = '2026-09-25T12:00:00Z';
		expect(upSince({ state: 'running', startedAt: at })).toBe(at);
		expect(upSince({ state: 'paused', startedAt: at })).toBe(at);
		expect(upSince({ state: 'exited', startedAt: at })).toBeNull();
		expect(upSince({ state: 'running' })).toBeNull();
	});

	it('sorts longer uptimes (earlier starts) after shorter ones, unknown last', () => {
		const earlier = uptimeSortValue('2026-09-25T10:00:00Z')!;
		const later = uptimeSortValue('2026-09-25T12:00:00Z')!;
		expect(later).toBeLessThan(earlier);
		expect(uptimeSortValue(undefined)).toBeNull();
		expect(uptimeSortValue('nope')).toBeNull();
	});
});

describe('detail pages in words (#22 polish)', () => {
	it('words restart policies, with the retries when known', () => {
		expect(restartPolicyLabel(undefined)).toBe('Never Restart');
		expect(restartPolicyLabel('no')).toBe('Never Restart');
		expect(restartPolicyLabel('unless-stopped')).toBe('Unless Stopped');
		expect(restartPolicyLabel('always')).toBe('Always');
		expect(restartPolicyLabel('on-failure')).toBe('On Failure');
		expect(restartPolicyLabel('on-failure', 0)).toBe('On Failure');
		expect(restartPolicyLabel('on-failure', 5)).toBe('On Failure (Up to 5 Retries)');
		expect(restartPolicyLabel('on-failure', 1)).toBe('On Failure (Up to 1 Retry)');
		expect(restartPolicyLabel('on-failure:3')).toBe('On Failure (Up to 3 Retries)');
		// The form offers the words, never the raw value in parentheses.
		expect(RESTART_OPTIONS.map((o) => o.label)).toEqual([
			'Never Restart',
			'On Failure',
			'Unless Stopped',
			'Always'
		]);
	});

	it('words health and shows a check without its CMD marker', () => {
		expect(healthLabel('healthy')).toBe('Healthy');
		expect(healthLabel('unhealthy')).toBe('Unhealthy');
		expect(healthLabel('starting')).toBe('Starting');
		expect(healthLabel('none')).toBe('No Health Check');
		expect(healthLabel(undefined)).toBe('No Health Check');
		expect(healthCommand(['CMD-SHELL', 'curl -f http://localhost/'])).toBe(
			'curl -f http://localhost/'
		);
		expect(healthCommand(['CMD', '/bin/check', '--quiet'])).toBe('/bin/check --quiet');
		expect(healthCommand(['NONE'])).toBe('');
		expect(healthCommand(undefined)).toBe('');
	});

	it('folds the labels Docker, Compose and image builders set', () => {
		expect(isSystemLabel('com.docker.compose.project')).toBe(true);
		expect(isSystemLabel('org.opencontainers.image.source')).toBe(true);
		expect(isSystemLabel('dev.neureka.docker-manager.stack')).toBe(true);
		expect(isSystemLabel('docker-manager.managed')).toBe(true);
		expect(isSystemLabel('docker-manager.depends_on')).toBe(true);
		// The opt-outs users set stay visible.
		expect(isSystemLabel('docker-manager.update.exclude')).toBe(false);
		expect(isSystemLabel('docker-manager.backup.exclude')).toBe(false);
		expect(isSystemLabel('docker-manager.maintenance.exclude')).toBe(false);
		expect(isSystemLabel('traefik.enable')).toBe(false);
		expect(
			splitLabels({
				'traefik.http.routers.web.rule': 'Host(`x`)',
				'com.docker.compose.service': 'web',
				'traefik.enable': 'true',
				'org.opencontainers.image.title': 'web'
			})
		).toEqual({
			user: [
				['traefik.enable', 'true'],
				['traefik.http.routers.web.rule', 'Host(`x`)']
			],
			system: [
				['com.docker.compose.service', 'web'],
				['org.opencontainers.image.title', 'web']
			]
		});
		expect(splitLabels(undefined)).toEqual({ user: [], system: [] });
	});

	it('shows each alias once, without the name, hostname or ID Docker adds', () => {
		const c = { name: 'web', id: '4e1b5f1a6d8e0123456789', hostname: '4e1b5f1a6d8e' };
		expect(networkAliases(['web', 'api', '4e1b5f1a6d8e', 'api', 'backend'], c)).toEqual([
			'api',
			'backend'
		]);
		expect(networkAliases(undefined, c)).toEqual([]);
		expect(hostnameIsId('4e1b5f1a6d8e', c.id)).toBe(true);
		expect(hostnameIsId('web-1', c.id)).toBe(false);
		expect(hostnameIsId(undefined, c.id)).toBe(false);
	});

	it('puts tagged images first and folds the untagged ones', () => {
		const a = { id: 'a', repoTags: ['nginx:1.27'] };
		const b = { id: 'b', repoTags: [] };
		const c = { id: 'c', repoTags: ['redis:7'] };
		expect(splitUntagged([b, a, c])).toEqual({ tagged: [a, c], untagged: [b] });
	});

	it('hides a column that repeats one value on every row', () => {
		expect(sameEverywhere([{ d: 'local' }, { d: 'local' }], (r) => r.d)).toBe(true);
		expect(sameEverywhere([{ d: 'local' }, { d: 'nfs' }], (r) => r.d)).toBe(false);
		expect(sameEverywhere([], (r: { d: string }) => r.d)).toBe(true);
	});

	it("lists a network's containers by name with their address on it", () => {
		expect(
			attachedContainers(
				'shop_default',
				[
					{ id: '2', name: 'web', state: 'running' },
					{ id: '1', name: 'db', state: 'running' },
					{ id: '3', name: 'gone' }
				],
				[
					{
						name: 'web',
						networks: [
							{ name: 'bridge', ipAddress: '172.17.0.2' },
							{
								name: 'shop_default',
								ipAddress: '172.20.0.3',
								ipv6Address: 'fd00::3'
							}
						]
					},
					{ name: 'db', networks: [{ name: 'shop_default', ipAddress: '172.20.0.2' }] }
				]
			)
		).toEqual([
			{ id: '1', name: 'db', state: 'running', addresses: ['172.20.0.2'] },
			{ id: '3', name: 'gone', addresses: [] },
			{ id: '2', name: 'web', state: 'running', addresses: ['172.20.0.3', 'fd00::3'] }
		]);
	});

	it("prefers the addresses the network's answer reports", () => {
		const refs = [
			{
				id: '2',
				name: 'web',
				state: 'running',
				ipAddress: '172.20.0.9',
				ipv6Address: 'fd00::9'
			},
			{ id: '1', name: 'db', state: 'running', ipAddress: '172.20.0.8' }
		];
		expect(
			attachedContainers('shop_default', refs, [
				{ name: 'web', networks: [{ name: 'shop_default', ipAddress: '172.20.0.3' }] }
			])
		).toEqual([
			{ id: '1', name: 'db', state: 'running', addresses: ['172.20.0.8'] },
			{ id: '2', name: 'web', state: 'running', addresses: ['172.20.0.9', 'fd00::9'] }
		]);
		// No containers list is needed when every address is known.
		expect(needsAddressLookup(refs)).toBe(false);
		expect(needsAddressLookup([...refs, { id: '3', name: 'old' }])).toBe(true);
		expect(needsAddressLookup(undefined)).toBe(false);
		expect(attachedContainers('shop_default', [{ id: '3', name: 'old' }])).toEqual([
			{ id: '3', name: 'old', addresses: [] }
		]);
	});
});
