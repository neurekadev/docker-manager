import { describe, expect, it } from 'vitest';
import {
	compactDuration,
	containerStatus,
	envLines,
	filterContainers,
	imagePresent,
	joinCommand,
	megabytes,
	normalizeReference,
	parsePairs,
	portHref,
	portText,
	shortDigest,
	splitCommand,
	uniquePorts,
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

describe('container list filters (#6: state, stack, label)', () => {
	type Row = {
		name: string;
		image: string;
		state: string;
		stack?: { project: string };
		labels: Record<string, string>;
	};
	const rows: Row[] = [
		{
			name: 'web',
			image: 'nginx',
			state: 'running',
			stack: { project: 'silo' },
			labels: { tier: 'front' }
		},
		{
			name: 'db',
			image: 'postgres:16',
			state: 'exited',
			stack: { project: 'silo' },
			labels: {}
		},
		{
			name: 'pihole',
			image: 'pihole/pihole',
			state: 'running',
			labels: { 'traefik.enable': 'true' }
		}
	];
	const f = { q: '', state: '', stack: '', label: '' };

	it('filters by text on name or image, state, stack and label', () => {
		expect(filterContainers(rows, { ...f, q: 'POSTGRES' }).map((c) => c.name)).toEqual(['db']);
		expect(filterContainers(rows, { ...f, state: 'running' }).map((c) => c.name)).toEqual([
			'web',
			'pihole'
		]);
		expect(filterContainers(rows, { ...f, stack: 'silo' }).map((c) => c.name)).toEqual([
			'web',
			'db'
		]);
		expect(filterContainers(rows, { ...f, stack: '-' }).map((c) => c.name)).toEqual(['pihole']);
		expect(filterContainers(rows, { ...f, label: 'tier' }).map((c) => c.name)).toEqual(['web']);
		expect(
			filterContainers(rows, { ...f, label: 'traefik.enable=true' }).map((c) => c.name)
		).toEqual(['pihole']);
		expect(filterContainers(rows, { ...f, label: 'tier=back' })).toEqual([]);
	});

	it('shows health while running', () => {
		expect(containerStatus({ state: 'running', health: 'unhealthy' })).toBe('unhealthy');
		expect(containerStatus({ state: 'running', health: 'healthy' })).toBe('running');
		expect(containerStatus({ state: 'exited', health: 'unhealthy' })).toBe('exited');
	});
});
