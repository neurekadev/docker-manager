// Container terminal session (#8, docs/api/streams.md "Container exec"):
//
//   1. POST …/exec-sessions {shell, tty, cols, rows} → {id, streamUrl,
//      subprotocol, ticket, command}; the agent finds the shell in the
//      container (auto: Bash if present, else sh; 422 command_not_found
//      when there is none) and command names what it started
//   2. WebSocket to streamUrl offering the subprotocols docker-manager.exec.v1 and
//      docker-manager.ticket.<ticket> (the one-use ticket never goes in the URL)
//   3. binary frames: first byte 0 = stdin (client→server), 1/2 =
//      stdout/stderr; text frames: {"type":"resize"} (client),
//      {"type":"exit"} / {"type":"error"} (server)
//   4. DELETE …/exec-sessions/{id} ends it; close codes explain the end
//      (4422: the shell does not exist in the image, reported by older
//      agents that cannot look it up first).
//
// The server closes idle sessions after 30 minutes; the terminal warns at 25.
// An open terminal is critical work (#23): the PWA update prompt does not
// reload it away. Terminal bytes are never stored or logged.
import { criticalWork } from '$lib/live';
import { socketUrl } from './url';

export const IDLE_WARNING_MS = 25 * 60_000;
export const IDLE_LIMIT_MS = 30 * 60_000;
export const SUBPROTOCOL = 'docker-manager.exec.v1';

export type TerminalState = 'idle' | 'creating' | 'connecting' | 'open' | 'closed' | 'failed';

export interface ExecTarget {
	environmentId: string;
	containerId: string;
	label: string;
}

export interface ExecSession {
	id: string;
	streamUrl: string;
	subprotocol: string;
	ticket: string;
	expiresAt: string;
	/** The argv the agent started (the resolved shell). */
	command?: string[];
}

/** The shells a terminal can open (the API's `shell`). */
export type Shell = 'auto' | 'bash' | 'sh' | 'zsh';

export const SHELLS: readonly { value: Shell; label: string }[] = [
	{ value: 'auto', label: 'Automatic' },
	{ value: 'bash', label: 'Bash' },
	{ value: 'sh', label: 'sh' },
	{ value: 'zsh', label: 'Zsh' }
];

/** The shell's name in sentences ("This container has no Bash"). */
export function shellName(shell: Shell): string {
	switch (shell) {
		case 'auto':
			return 'Bash or sh';
		case 'bash':
			return 'Bash';
		case 'zsh':
			return 'Zsh';
		default:
			return 'sh';
	}
}

/** The parts of WebSocket the session uses (fakeable in tests). */
export interface WebSocketLike {
	binaryType: string;
	readonly readyState: number;
	readonly protocol: string;
	send(data: string | ArrayBufferLike | Uint8Array): void;
	close(code?: number, reason?: string): void;
	onopen: ((ev: Event) => void) | null;
	onmessage: ((ev: MessageEvent) => void) | null;
	onclose: ((ev: CloseEvent | { code: number; reason: string }) => void) | null;
	onerror: ((ev: Event) => void) | null;
}

export interface TerminalDeps {
	create(
		t: ExecTarget,
		body: { shell: Shell; tty: boolean; cols: number; rows: number }
	): Promise<ExecSession>;
	remove(t: ExecTarget, sessionId: string): Promise<void>;
	socket(url: string, protocols: string[]): WebSocketLike;
	now?: () => number;
	setInterval?: (fn: () => void, ms: number) => unknown;
	clearInterval?: (h: unknown) => void;
}

/** What a close code means for the user (docs/api/streams.md). */
export function closeMessage(
	code: number,
	o: { exitCode?: number | null; shell?: Shell } = {}
): string {
	switch (code) {
		case 1000:
			return o.exitCode != null
				? `The process exited with code ${o.exitCode}.`
				: 'The session ended.';
		case 1001:
			return 'Docker Manager is restarting. Connect again to start a new session.';
		case 1008:
			return 'The terminal was refused. Reload the page and connect again.';
		case 1009:
			return 'The input was too large for the terminal. Connect again.';
		case 1011:
			return 'The terminal failed on the manager. Connect again.';
		case 4401:
			return 'Your session ended. Sign in again to open a terminal.';
		case 4403:
			return 'Your permission to open terminals in this container was removed.';
		case 4404:
			return 'The session expired before it connected. Connect again.';
		case 4408:
			return 'The terminal closed after 30 minutes without activity (or 8 hours in total). Connect again to continue.';
		case 4409:
			return 'This terminal is already open in another tab or window.';
		case 4422:
			return o.shell
				? `This container has no ${shellName(o.shell)} — choose another shell.`
				: 'This container has no such shell — choose another shell.';
		case 4503:
			return "The environment went offline. Connect again when it's back.";
		default:
			return 'The connection closed unexpectedly. Connect again to start a new session.';
	}
}

/** Whether "Connect again" can help after this close code. */
export function canReconnect(code: number): boolean {
	return ![4403, 4409].includes(code);
}

const enc = new TextEncoder();

export class ExecTerminal {
	state = $state<TerminalState>('idle');
	closeCode = $state<number | null>(null);
	exitCode = $state<number | null>(null);
	/** The end or failure, in plain language. */
	message = $state<string | null>(null);
	/** No input or output for 25 minutes: it closes at 30. */
	idleWarning = $state(false);
	/** Seconds until the idle close while warning. */
	idleSecondsLeft = $state(0);
	/** The command the session started ("/bin/bash"), once created. */
	command = $state<string | null>(null);

	/** Output bytes for the terminal (set by the view). */
	onoutput: ((data: Uint8Array) => void) | null = null;

	#deps: TerminalDeps;
	#target: ExecTarget | null = null;
	#shell: Shell = 'auto';
	#ws: WebSocketLike | null = null;
	#session: ExecSession | null = null;
	#last = 0;
	#timer: unknown = null;
	#release: (() => void) | null = null;
	#ended = false;

	constructor(deps: TerminalDeps) {
		this.#deps = deps;
	}

	get sessionId(): string | null {
		return this.#session?.id ?? null;
	}

	#now() {
		return this.#deps.now?.() ?? Date.now();
	}

	/** Creates an exec session and attaches to it. */
	async connect(t: ExecTarget, shell: Shell, size: { cols: number; rows: number }) {
		if (this.state === 'creating' || this.state === 'connecting' || this.state === 'open')
			return;
		this.#target = t;
		this.#shell = shell;
		this.#ended = false;
		this.closeCode = null;
		this.exitCode = null;
		this.message = null;
		this.command = null;
		this.state = 'creating';
		let s: ExecSession;
		try {
			s = await this.#deps.create(t, {
				shell,
				tty: true,
				cols: size.cols,
				rows: size.rows
			});
		} catch (e) {
			this.state = 'failed';
			this.message = e instanceof Error ? e.message : String(e);
			throw e;
		}
		this.#session = s;
		this.command = s.command?.length ? s.command.join(' ') : null;
		this.state = 'connecting';
		const url = socketUrl(s.streamUrl, globalThis.location?.href ?? 'http://localhost/');
		const ws = this.#deps.socket(url, [
			s.subprotocol || SUBPROTOCOL,
			`docker-manager.ticket.${s.ticket}`
		]);
		ws.binaryType = 'arraybuffer';
		this.#ws = ws;
		this.#release = criticalWork.register('terminal', t.label);
		ws.onopen = () => {
			this.state = 'open';
			this.#touch();
			this.#startIdleTimer();
			this.resize(size.cols, size.rows);
		};
		ws.onmessage = (ev) => this.#onMessage(ev);
		ws.onclose = (ev) => this.#onClose(ev.code);
		ws.onerror = () => {
			// A close event with the code follows.
		};
	}

	#onMessage(ev: MessageEvent) {
		this.#touch();
		if (typeof ev.data === 'string') {
			try {
				const m = JSON.parse(ev.data) as {
					type: string;
					code?: number | string;
					message?: string;
				};
				if (m.type === 'exit' && typeof m.code === 'number') this.exitCode = m.code;
				else if (m.type === 'error') this.message = m.message ?? String(m.code);
			} catch {
				// not a control message
			}
			return;
		}
		const buf = new Uint8Array(ev.data as ArrayBuffer);
		if (buf.length < 1 || (buf[0] !== 1 && buf[0] !== 2)) return;
		this.onoutput?.(buf.subarray(1));
	}

	#onClose(code: number) {
		if (this.#ended) return;
		this.#ended = true;
		this.#stopIdleTimer();
		this.#ws = null;
		this.closeCode = code;
		this.state = code === 1000 || code === 1001 ? 'closed' : 'failed';
		this.message = closeMessage(code, { exitCode: this.exitCode, shell: this.#shell });
		this.#release?.();
		this.#release = null;
	}

	/** Keystrokes from the terminal. */
	send(data: string) {
		if (this.state !== 'open' || !this.#ws) return;
		const bytes = enc.encode(data);
		const frame = new Uint8Array(bytes.length + 1);
		frame[0] = 0;
		frame.set(bytes, 1);
		this.#ws.send(frame);
		this.#touch();
	}

	resize(cols: number, rows: number) {
		if (this.state !== 'open' || !this.#ws || cols < 1 || rows < 1) return;
		this.#ws.send(JSON.stringify({ type: 'resize', cols, rows }));
	}

	/** Ends the session (DELETE, then the socket closes with 1000). */
	async disconnect() {
		const t = this.#target;
		const s = this.#session;
		if (t && s && (this.state === 'open' || this.state === 'connecting')) {
			try {
				await this.#deps.remove(t, s.id);
			} catch {
				// Closing the socket ends the session as well.
			}
		}
		this.#ws?.close(1000, 'closed by the user');
		if (!this.#ended) this.#onClose(1000);
	}

	#touch() {
		this.#last = this.#now();
		if (this.idleWarning) this.idleWarning = false;
	}

	#startIdleTimer() {
		const set = this.#deps.setInterval ?? ((fn, ms) => setInterval(fn, ms));
		this.#timer = set(() => this.checkIdle(), 15_000);
	}

	#stopIdleTimer() {
		if (this.#timer === null) return;
		(this.#deps.clearInterval ?? ((h) => clearInterval(h as ReturnType<typeof setInterval>)))(
			this.#timer
		);
		this.#timer = null;
		this.idleWarning = false;
	}

	/** Runs every 15 s while open (exported for tests). */
	checkIdle() {
		if (this.state !== 'open') return;
		const idle = this.#now() - this.#last;
		this.idleWarning = idle >= IDLE_WARNING_MS;
		this.idleSecondsLeft = Math.max(0, Math.ceil((IDLE_LIMIT_MS - idle) / 1000));
	}
}
