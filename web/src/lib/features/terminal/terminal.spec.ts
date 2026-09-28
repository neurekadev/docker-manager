// Container terminal sessions (#8): the shell choice, the ticket travels in
// the subprotocol, binary stdin/stdout framing, resize messages, close codes
// (4422), the idle warning at 25 minutes, critical work while open.
import { describe, expect, it } from 'vitest';
import { criticalWork } from '$lib/live';
import {
	canReconnect,
	closeMessage,
	ExecTerminal,
	IDLE_WARNING_MS,
	SHELLS,
	shellName,
	type WebSocketLike
} from './session.svelte';
import { socketUrl } from './url';

class FakeSocket implements WebSocketLike {
	binaryType = 'blob';
	readyState = 0;
	protocol = 'docker-manager.exec.v1';
	sent: (string | Uint8Array)[] = [];
	closedWith: number | null = null;
	onopen: ((ev: Event) => void) | null = null;
	onmessage: ((ev: MessageEvent) => void) | null = null;
	onclose: ((ev: { code: number; reason: string }) => void) | null = null;
	onerror: ((ev: Event) => void) | null = null;

	constructor(
		readonly url: string,
		readonly protocols: string[]
	) {}
	send(d: string | ArrayBufferLike | Uint8Array) {
		this.sent.push(typeof d === 'string' ? d : new Uint8Array(d as ArrayBuffer));
	}
	close(code = 1000) {
		this.closedWith = code;
		this.onclose?.({ code, reason: '' });
	}
	open() {
		this.readyState = 1;
		this.onopen?.(new Event('open'));
	}
	message(data: string | ArrayBuffer) {
		this.onmessage?.({ data } as MessageEvent);
	}
	serverClose(code: number) {
		this.onclose?.({ code, reason: '' });
	}
}

function setup() {
	let now = 0;
	let tick: (() => void) | null = null;
	const sockets: FakeSocket[] = [];
	const created: unknown[] = [];
	const removed: string[] = [];
	const t = new ExecTerminal({
		create: async (_target, body) => {
			created.push(body);
			return {
				id: 's1',
				streamUrl: '/api/v1/environments/e1/containers/web/exec-sessions/s1/stream',
				subprotocol: 'docker-manager.exec.v1',
				ticket: 'tkt',
				expiresAt: '',
				command: ['/usr/bin/bash']
			};
		},
		remove: async (_target, id) => void removed.push(id),
		socket: (url, protocols) => {
			const s = new FakeSocket(url, protocols);
			sockets.push(s);
			return s;
		},
		now: () => now,
		setInterval: (fn) => ((tick = fn), 1),
		clearInterval: () => (tick = null)
	});
	return {
		t,
		sockets,
		created,
		removed,
		advance: (ms: number) => {
			now += ms;
			tick?.();
		},
		hasTimer: () => tick !== null
	};
}

const target = { environmentId: 'e1', containerId: 'web', label: 'silo-web' };

describe('terminal helpers', () => {
	it('names the shells, builds the socket URL and explains close codes', () => {
		expect(SHELLS.map((s) => s.value)).toEqual(['auto', 'bash', 'sh', 'zsh']);
		expect(SHELLS.map((s) => s.label)).toEqual(['Detect automatically', 'Bash', 'sh', 'Zsh']);
		expect(shellName('auto')).toBe('Bash or sh');
		expect(shellName('zsh')).toBe('Zsh');
		expect(socketUrl('/api/v1/x/stream', 'https://docker.example/stacks/1')).toBe(
			'wss://docker.example/api/v1/x/stream'
		);
		expect(socketUrl('/api/v1/x/stream', 'http://localhost:8080/')).toBe(
			'ws://localhost:8080/api/v1/x/stream'
		);
		expect(closeMessage(4422, { shell: 'bash' })).toBe(
			'This container has no Bash — choose another shell.'
		);
		expect(closeMessage(4422)).toBe('This container has no such shell — choose another shell.');
		expect(closeMessage(1000, { exitCode: 3 })).toBe('The process exited with code 3.');
		expect(closeMessage(4408)).toMatch(/30 minutes without activity/);
		expect(closeMessage(4409)).toMatch(/already open/);
		expect(closeMessage(1006)).toMatch(/closed unexpectedly/);
		expect(canReconnect(4422)).toBe(true);
		expect(canReconnect(4403)).toBe(false);
	});
});

describe('ExecTerminal', () => {
	it('connects with the ticket in the subprotocol and frames stdin, stdout and resize', async () => {
		const { t, sockets, created } = setup();
		const before = criticalWork.items.length;
		const out: string[] = [];
		t.onoutput = (b) => out.push(new TextDecoder().decode(b));
		expect(t.command).toBeNull();
		await t.connect(target, 'auto', { cols: 100, rows: 30 });
		expect(created).toEqual([{ shell: 'auto', tty: true, cols: 100, rows: 30 }]);
		expect(t.command).toBe('/usr/bin/bash');
		const ws = sockets[0];
		// Node has no page URL: the default base is http://localhost/.
		expect(ws.url).toBe(
			'ws://localhost/api/v1/environments/e1/containers/web/exec-sessions/s1/stream'
		);
		expect(ws.url).not.toContain('tkt');
		expect(ws.protocols).toEqual(['docker-manager.exec.v1', 'docker-manager.ticket.tkt']);
		expect(ws.binaryType).toBe('arraybuffer');
		expect(t.state).toBe('connecting');
		expect(criticalWork.items.at(-1)).toMatchObject({ kind: 'terminal', label: 'silo-web' });
		ws.open();
		expect(t.state).toBe('open');
		expect(ws.sent[0]).toBe('{"type":"resize","cols":100,"rows":30}');
		t.send('ls\r');
		expect(Array.from(ws.sent[1] as Uint8Array)).toEqual([0, 108, 115, 13]);
		t.resize(120, 40);
		expect(ws.sent[2]).toBe('{"type":"resize","cols":120,"rows":40}');
		ws.message(new Uint8Array([1, 104, 105]).buffer);
		ws.message(new Uint8Array([2, 33]).buffer);
		ws.message(new Uint8Array([0, 120]).buffer); // not server output
		expect(out).toEqual(['hi', '!']);
		ws.message('{"type":"exit","code":0}');
		ws.serverClose(1000);
		expect(t.state).toBe('closed');
		expect(t.message).toBe('The process exited with code 0.');
		expect(criticalWork.items.length).toBe(before);
		t.send('ignored');
		expect(ws.sent).toHaveLength(3);
	});

	it('reports a missing shell (4422) and ends sessions the user closes', async () => {
		const { t, sockets, removed } = setup();
		await t.connect(target, 'zsh', { cols: 80, rows: 24 });
		sockets[0].open();
		sockets[0].message('{"type":"error","code":"command_not_found","message":"not found"}');
		sockets[0].serverClose(4422);
		expect(t.state).toBe('failed');
		expect(t.closeCode).toBe(4422);
		expect(t.message).toBe('This container has no Zsh — choose another shell.');

		await t.connect(target, 'sh', { cols: 80, rows: 24 });
		sockets[1].open();
		await t.disconnect();
		expect(removed).toEqual(['s1']);
		expect(sockets[1].closedWith).toBe(1000);
		expect(t.state).toBe('closed');
	});

	it('warns at 25 idle minutes; activity clears the warning', async () => {
		const { t, sockets, advance, hasTimer } = setup();
		await t.connect(target, 'auto', { cols: 80, rows: 24 });
		sockets[0].open();
		expect(hasTimer()).toBe(true);
		advance(IDLE_WARNING_MS - 1000);
		expect(t.idleWarning).toBe(false);
		advance(1000);
		expect(t.idleWarning).toBe(true);
		expect(t.idleSecondsLeft).toBe(300);
		t.send('x');
		expect(t.idleWarning).toBe(false);
		advance(IDLE_WARNING_MS + 60_000);
		expect(t.idleSecondsLeft).toBe(240);
		sockets[0].message(new Uint8Array([1, 65]).buffer);
		expect(t.idleWarning).toBe(false);
		sockets[0].serverClose(4408);
		expect(hasTimer()).toBe(false);
		expect(t.message).toMatch(/30 minutes without activity/);
	});

	it('surfaces create failures without opening a socket', async () => {
		const t = new ExecTerminal({
			create: async () => {
				throw new Error("silo-web isn't running. Start it to open a terminal.");
			},
			remove: async () => {},
			socket: () => {
				throw new Error('must not connect');
			}
		});
		await expect(t.connect(target, 'auto', { cols: 80, rows: 24 })).rejects.toThrow(
			/isn't running/
		);
		expect(t.state).toBe('failed');
		expect(t.message).toMatch(/isn't running/);
	});
});
