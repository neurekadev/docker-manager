// Moving Docker Manager to a new server (docs/internal/architecture/manager-move.md):
// the words of the move's states on the old manager, the shell banner of a
// locked manager, the old manager's wizard (Settings → Move to a new
// server: the new server's checklist, the check's reminders, Move
// everything's progress, the moved panel), the new manager's status page
// (waiting mode) and Move complete. Pure (model.spec.ts); new states get
// their words here first.
import type { Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui';
import { formatBytes } from '$lib/ui/format';

export type ManagerMove = Schema<'ManagerMove'>;
export type MoveState = ManagerMove['state'];
export type ManagerMoveDefaults = Schema<'ManagerMoveDefaults'>;
export type CreatedManagerMove = Schema<'CreatedManagerMove'>;
export type MoveRedirect = Schema<'ManagerMoveRedirect'>;
/** The new manager's public status (GET /move/status). */
export type MoveStatus = Schema<'MoveStatus'>;
export type WaitPhase = MoveStatus['phase'];
/** The move lock as every signed-in user reads it (GET /auth/session). */
export type MoveLock = Schema<'SessionManagerMove'>['state'];

/** The address shown when neither the settings nor the browser know one. */
export const EXAMPLE_ADDRESS = 'https://docker.example.com';

/**
 * This Docker Manager's address: its public URL when the settings name it,
 * else the address the browser opened it at.
 */
export function thisAddress(publicUrl?: string | null, origin?: string | null): string {
	return publicUrl || origin || EXAMPLE_ADDRESS;
}

/**
 * States of a move under way on this manager (created, not ended, not
 * arrived). Live events (topic manager) keep the move pages current.
 */
export const ACTIVE_STATES: readonly MoveState[] = [
	'open',
	'moving',
	'ready',
	'draining',
	'handed_off'
];

/** States in which this manager is read-only (every change answers manager_moved). */
export const LOCKED_STATES: readonly MoveState[] = ['draining', 'handed_off', 'confirmed'];

export function isLocked(state: MoveState | undefined): boolean {
	return !!state && LOCKED_STATES.includes(state);
}

/** How the old manager shows its move. */
export interface MoveStatusView {
	/** The badge text. */
	label: string;
	tone: BadgeTone;
	pulse: boolean;
	title: string;
	/** cancel: "Cancel move" (until the handoff); resume: "Resume on this server" (handed off). */
	stop: 'cancel' | 'resume' | null;
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** The old manager's move in words. */
export function moveStatus(move: ManagerMove): MoveStatusView {
	switch (move.state) {
		case 'open':
			return {
				label: 'Waiting for the new server',
				tone: 'info',
				pulse: true,
				title: 'Waiting for the new server',
				stop: 'cancel'
			};
		case 'moving':
			return {
				label: 'Moving apps',
				tone: 'info',
				pulse: true,
				title: 'Moving your apps to the new server',
				stop: 'cancel'
			};
		case 'ready':
			return {
				label: 'Ready',
				tone: 'info',
				pulse: true,
				title: 'Your apps moved: handing Docker Manager over',
				stop: 'cancel'
			};
		case 'draining':
			return {
				label: 'Locked',
				tone: 'warn',
				pulse: true,
				title:
					move.jobsRunning > 0
						? `Locked: finishing ${plural(move.jobsRunning, 'running job', 'running jobs')}`
						: 'Locked: handing over',
				stop: 'cancel'
			};
		case 'handed_off':
			return {
				label: 'Handed over',
				tone: 'info',
				pulse: true,
				title: 'Handed over: waiting for the new Docker Manager to confirm',
				stop: 'resume'
			};
		case 'confirmed':
			return { label: 'Moved', tone: 'ok', pulse: false, title: 'Moved', stop: null };
		case 'cancelled':
			return {
				label: 'Cancelled',
				tone: 'neutral',
				pulse: false,
				title: 'The move was cancelled',
				stop: null
			};
		case 'expired':
			return {
				label: 'Expired',
				tone: 'neutral',
				pulse: false,
				title: 'The move expired',
				stop: null
			};
		case 'arrived':
			return {
				label: 'Arrived',
				tone: 'ok',
				pulse: false,
				title: 'Docker Manager moved here',
				stop: null
			};
	}
}

/** The shell's banner while this manager is read-only because it moves. */
export interface MoveBanner {
	title: string;
	body: string;
}

/** The lock of the owner's move state (the session says it for everyone). */
export function lockOf(state: MoveState | undefined): MoveLock | undefined {
	if (state === undefined) return undefined;
	if (state === 'confirmed') return 'moved';
	if (state === 'draining' || state === 'handed_off') return 'moving';
	return 'none';
}

/**
 * The banner of a locked manager: from the lock every session reads (the
 * owner's move state first) or, as a fallback, from a change the
 * manager refused with manager_moved. Null while nothing is locked.
 */
export function moveBanner(
	lock: MoveLock | undefined,
	refused: boolean,
	address: string
): MoveBanner | null {
	if (lock === 'moved')
		return {
			title: 'This Docker Manager moved to a new server',
			body: `Nothing can be changed here. Use ${address} once it leads to the new server.`
		};
	if (lock === 'moving' || refused)
		return {
			title: 'This Docker Manager is moving to a new server',
			body: 'Nothing can be changed here. Your apps keep running.'
		};
	return null;
}

// ---------------------------------------------------------------------------
// The old manager's wizard (Settings → Move to a new server).

/** The job kind of Move everything (the running list brings it back after a reload). */
export const MOVE_JOB_KIND = 'manager.move';

/** The wizard's steps, in order. */
export const MOVE_STEPS = [
	{
		id: 'server',
		label: 'New server',
		description:
			'Docker Manager creates the setup files for the new server. You start them there.'
	},
	{
		id: 'check',
		label: 'Check',
		description: 'What moves to the new server, and in which order. Nothing stops yet.'
	},
	{
		id: 'move',
		label: 'Move',
		description:
			'Your apps move first. Then Docker Manager hands itself over to the new server.'
	}
] as const;

export type MoveStepId = (typeof MOVE_STEPS)[number]['id'];

/** A move that is under way on this manager (created, not ended, not arrived). */
export function isActive(move: ManagerMove | null | undefined): move is ManagerMove {
	return !!move && ACTIVE_STATES.includes(move.state);
}

/** The move was handed over (or confirmed): the wizard gives way to the moved panel. */
export function isHandedOver(move: ManagerMove | null | undefined): boolean {
	return move?.state === 'handed_off' || move?.state === 'confirmed';
}

/** Where the wizard opens for a move (after a reload or coming back). */
export function stepOf(move: ManagerMove | null | undefined): MoveStepId {
	if (!isActive(move)) return 'server';
	if (move.state === 'open') return move.progress?.jobId ? 'move' : 'server';
	return 'move';
}

/** One line of the new server's checklist. */
export interface ChecklistItem {
	label: string;
	done: boolean;
	/** What to do when it cannot happen by itself. */
	detail?: string;
}

/** Where the new server stands: its agent connected, its Docker Manager waiting. */
export function newServerChecklist(move: ManagerMove): ChecklistItem[] {
	const n = move.newServer;
	const expired = n?.enrollmentState === 'expired' || n?.enrollmentState === 'revoked';
	return [
		{
			label: "New server's agent connected",
			done: !!n?.online,
			detail: !n?.online && expired ? SETUP_FILES_EXPIRED : undefined
		},
		{ label: 'New Docker Manager is waiting', done: !!n?.managerCheckedIn }
	];
}

/** Both checklist items are done: the wizard may go on. */
export function newServerReady(move: ManagerMove | null | undefined): boolean {
	return !!move && newServerChecklist(move).every((i) => i.done);
}

/** The checklist's words when the new server's enrollment token ran out. */
export const SETUP_FILES_EXPIRED = 'The setup files expired. Create new setup files.';

/** Why the New server step offers "Create new setup files". */
export type SetupFilesReason = 'expired' | 'not_shown';

/**
 * Whether the New server step offers new setup files: while the move is
 * open and the new server is not ready yet, when the files are no longer
 * on the page (a reload, coming back) or its enrollment token ran out.
 */
export function setupFilesReason(move: ManagerMove, shown: boolean): SetupFilesReason | null {
	if (move.state !== 'open' || newServerReady(move)) return null;
	if (newServerChecklist(move)[0].detail === SETUP_FILES_EXPIRED) return 'expired';
	return shown ? null : 'not_shown';
}

/** The notice above "Create new setup files". */
export function setupFilesNotice(reason: SetupFilesReason): { title: string; body: string } {
	return reason === 'expired'
		? {
				title: 'The setup files expired.',
				body: "The new server's agent can no longer connect with them. Create new setup files and use them on the new server instead."
			}
		: {
				title: 'The setup files were shown when you created the move.',
				body: 'If you no longer have them, create new ones. The old files then stop working.'
			};
}

/** The new server's agent already connected with the move's token (it keeps its connection). */
export function agentEnrolled(move: ManagerMove): boolean {
	return !!move.newServer?.environmentId;
}

/** What "Create new setup files" does, for its confirmation. */
export function setupFilesConsequences(move: ManagerMove): string[] {
	return [
		'The setup files you have now stop working. Docker Manager on the new server must use the new ones.',
		agentEnrolled(move)
			? 'The agent on the new server stays connected.'
			: "The new server's agent gets a new enrollment token, valid for 24 hours.",
		'On the new server, replace both files in the same folder, then run docker compose up -d.'
	];
}

/** The folder the one-command setup creates on the new server. */
export const SETUP_FOLDER = 'docker-manager';

const HEREDOC_BASE = 'DOCKER_MANAGER_EOF';

/**
 * A here-document delimiter that is not a line of content: the base, else
 * the base with the first free number (DOCKER_MANAGER_EOF_2, ...).
 */
export function heredocDelimiter(content: string, base = HEREDOC_BASE): string {
	const lines = new Set(content.split('\n').map((l) => l.replace(/\r$/, '')));
	let d = base;
	for (let i = 2; lines.has(d); i++) d = `${base}_${i}`;
	return d;
}

/** `cat > file` from a quoted here-document: the content stays literal ($, `, \ included). */
function writeFile(file: string, content: string): string[] {
	const body = content.replace(/\r\n?/g, '\n');
	const d = heredocDelimiter(body);
	const text = body.endsWith('\n') ? body.slice(0, -1) : body;
	return [`cat > ${file} <<'${d}'`, text, d];
}

/**
 * The new server's setup in one paste: a subshell that stops at the first
 * failure (set -e; the person's own shell stays open), creates the folder,
 * writes compose.yaml and .env from quoted here-documents (nothing in them
 * is expanded), keeps .env readable only by its owner (created empty with
 * mode 600 before the secrets are written) and starts Docker Manager.
 * The files hold the pairing code and the enrollment token: the text lives
 * only in the page, like the files.
 */
export function setupScript(
	files: { composeYaml: string; env: string },
	folder = SETUP_FOLDER
): string {
	return [
		'(',
		'set -e',
		`mkdir -p ${folder}`,
		`cd ${folder}`,
		...writeFile('compose.yaml', files.composeYaml),
		'touch .env',
		'chmod 600 .env',
		...writeFile('.env', files.env),
		'docker compose up -d',
		')'
	].join('\n');
}

/** Said with new setup files when the new server's agent keeps its connection. */
export const AGENT_KEPT =
	'The new .env has no enrollment token, because the agent keeps its connection. On the new server, replace the .env in the folder you used before and run docker compose up -d, or paste the command again.';

/** The host of an address without its port ("192.168.1.20:8080" → "192.168.1.20"). */
export function hostOnly(address: string | undefined): string {
	const a = (address ?? '').trim();
	const v6 = a.match(/^\[([^\]]+)\](?::\d+)?$/);
	if (v6) return v6[1];
	const i = a.lastIndexOf(':');
	return i > 0 && a.indexOf(':') === i && /^\d+$/.test(a.slice(i + 1)) ? a.slice(0, i) : a;
}

/** The host name users point DNS at: the public URL's host, else nothing known. */
export function dnsName(publicUrl: string | null | undefined): string | null {
	if (!publicUrl) return null;
	try {
		return new URL(publicUrl).hostname || null;
	} catch {
		return null;
	}
}

/**
 * The check step's "Before you start" reminders (the page adds "Follow the
 * move at <status address> meanwhile." with the link).
 */
export const BEFORE_YOU_START: readonly string[] = [
	'Your usual address stops working while your reverse proxy moves, until you point DNS at the new server.',
	"If your reverse proxy sends Docker Manager's address to this server's IP, change it to the new server's IP."
];

/** Said when there are no apps to move (no environment next to Docker Manager, or no stacks). */
export const ONLY_MANAGER_MOVES =
	'Only Docker Manager moves: there are no apps on this server to move.';

/** What the check's answer needs to say whether anything moves. */
export interface CheckOutcome {
	allowed: boolean;
	stacks: readonly unknown[];
	blockers: readonly { code: string }[];
}

/** The check found nothing to move (its only finding says so). */
export function nothingToMove(p: CheckOutcome): boolean {
	return p.stacks.length === 0 && p.blockers.every((b) => b.code === 'no_stacks');
}

/** No apps move: no environment next to Docker Manager, no stacks there, or a check that found none. */
export function onlyManagerMoves(move: ManagerMove, preview: CheckOutcome | null): boolean {
	if (!move.sourceEnvironment || move.sourceEnvironment.stackCount === 0) return true;
	return !!preview && nothingToMove(preview);
}

/** Move everything may start: nothing to move, or a check that allows it. */
export function canMoveEverything(move: ManagerMove, preview: CheckOutcome | null): boolean {
	if (onlyManagerMoves(move, preview)) return true;
	return !!preview?.allowed;
}

/** Where Move everything stands. */
export type RunPhase = 'idle' | 'moving' | 'failed' | 'handing_over' | 'handed_over' | 'moved';

export interface RunView {
	phase: RunPhase;
	title: string;
	detail?: string;
	/** Stacks moved of all (moving). */
	moved: number;
	total: number;
	/** What to do after a failure. */
	recovery?: string;
}

const RUNNING_JOB: readonly string[] = ['queued', 'blocked', 'dispatched', 'running', 'cancelling'];

const RUN_ERRORS: Record<string, string> = {
	manager_move_apps_blocked: 'Some apps cannot move yet',
	manager_move_apps_not_moved: 'Not every app moved'
};

/** Move everything in words, from the move alone (it survives a reload). */
export function runView(move: ManagerMove): RunView {
	const p = move.progress;
	const base = { moved: p?.stacksMoved ?? 0, total: p?.stacksTotal ?? 0 };
	switch (move.state) {
		case 'confirmed':
			return { ...base, phase: 'moved', title: 'Handed over' };
		case 'handed_off':
			return { ...base, phase: 'handed_over', title: 'Handed over' };
		case 'ready':
			return {
				...base,
				phase: 'handing_over',
				title: 'Handing over Docker Manager…',
				detail: move.newServer?.managerCheckedIn === false ? NOT_ASKING : undefined
			};
		case 'draining':
			return {
				...base,
				phase: 'handing_over',
				title: 'Handing over Docker Manager…',
				detail:
					move.jobsRunning > 0
						? `Waiting for ${plural(move.jobsRunning, 'running job', 'running jobs')} to finish.`
						: undefined
			};
		case 'moving':
			return movingView(move, base);
		case 'open':
			if (p?.jobState && RUNNING_JOB.includes(p.jobState)) return movingView(move, base);
			if (p?.jobState && p.jobState !== 'succeeded')
				return {
					...base,
					phase: 'failed',
					title: (p.errorCode && RUN_ERRORS[p.errorCode]) || 'The move stopped',
					recovery: p.recovery || 'Fix the cause, then try again.'
				};
			return { ...base, phase: 'idle', title: 'Ready to move' };
		default:
			return { ...base, phase: 'idle', title: 'Ready to move' };
	}
}

function movingView(move: ManagerMove, base: { moved: number; total: number }): RunView {
	const current = move.progress?.currentStack;
	return {
		...base,
		phase: 'moving',
		title: base.total
			? `Moving your apps: ${base.moved} of ${plural(base.total, 'stack', 'stacks')}`
			: 'Moving your apps…',
		detail: current ? `Now moving ${current}.` : undefined
	};
}

/** The wizard's main button on the Move step. */
export function moveButtonLabel(phase: RunPhase): string {
	if (phase === 'failed') return 'Try again';
	if (phase === 'moving' || phase === 'handing_over') return 'Moving…';
	return 'Move everything';
}

/**
 * Whether ending the move restarts this Docker Manager: once agents heard
 * the new address (a redirect was sent) or the state was handed off, the
 * manager raises its generation and restarts.
 */
export function restartsWhenEnded(move: ManagerMove): boolean {
	return move.state === 'handed_off' || move.redirects.some((r) => r.sent);
}

/** Said while the move is ready but the new Docker Manager stopped asking for it. */
export const NOT_ASKING =
	'The new Docker Manager has not asked for the handoff in the last two minutes. Check that it still runs on the new server.';

/** What "Cancel the move" does, for its confirmation. */
export function cancelConsequences(move: ManagerMove | null): string[] {
	const out = [
		'Docker Manager stays on this server and works as before.',
		'Apps that already moved stay on the new server.',
		'The setup files stop working. On the new server, stop the waiting Docker Manager with docker compose stop docker-manager. Its agent keeps managing the apps that moved.'
	];
	if (move && restartsWhenEnded(move))
		out.push('Docker Manager restarts, because its agents already heard the new address.');
	return out;
}

/** The panel once the move was handed over. */
export interface MovedPanel {
	title: string;
	body: string;
	statusUrl?: string;
}

/**
 * After the handoff: where to point DNS and where to follow it. `publicUrl`
 * is this Docker Manager's public URL (the new one shares it).
 */
export function movedPanel(move: ManagerMove, publicUrl: string | null | undefined): MovedPanel {
	const host = hostOnly(move.newServerAddress) || 'the new server';
	const name = dnsName(publicUrl);
	const body = name
		? `Point DNS for ${name} at the new server (${host}).`
		: `Open Docker Manager on the new server (${host}) from now on.`;
	return { title: 'Docker Manager moved.', body, statusUrl: move.statusUrl };
}

// ---------------------------------------------------------------------------
// The new manager's status page (waiting mode, public).

/** Phases in which the new manager waits for the move: every page shows the status page. */
export function isWaitingPhase(phase: WaitPhase | undefined): boolean {
	return !!phase && phase !== 'none' && phase !== 'complete';
}

export type WaitStepId = 'wait' | 'apps' | 'jobs' | 'copy' | 'check' | 'restart' | 'done';
const WAIT_ORDER: readonly WaitStepId[] = [
	'wait',
	'apps',
	'jobs',
	'copy',
	'check',
	'restart',
	'done'
];

export interface WaitStep {
	id: WaitStepId;
	label: string;
	state: 'done' | 'current' | 'todo' | 'failed';
}

/** The step the move is at, from the phase and the old manager's last answer. */
export function waitStepOf(s: MoveStatus): WaitStepId {
	switch (s.phase) {
		case 'complete':
			return 'done';
		case 'restarting':
			return 'restart';
		case 'checking':
		case 'staging':
		case 'failed':
			return 'check';
		case 'copying':
			return 'copy';
		case 'finishing_jobs':
			return 'jobs';
		case 'waiting':
		case 'connecting':
			switch (s.oldState) {
				case 'moving':
					return 'apps';
				case 'ready':
				case 'draining':
					return 'jobs';
				case 'handed_off':
					return 'copy';
				default:
					return 'wait';
			}
		default:
			return 'wait';
	}
}

function waitLabel(id: WaitStepId, s: MoveStatus, current: boolean): string {
	switch (id) {
		case 'wait':
			return 'Waiting for the old server';
		case 'apps': {
			if (!s.stacksTotal) return 'Moving your apps';
			const now = current && s.currentStack ? `, now: ${s.currentStack}` : '';
			return `Moving your apps (${s.stacksMoved} of ${s.stacksTotal}${now})`;
		}
		case 'jobs':
			return 'Finishing running jobs';
		case 'copy':
			return s.bytesTotal > 0
				? `Copying Docker Manager (${formatBytes(s.bytesReceived)} of ${formatBytes(s.bytesTotal)})`
				: 'Copying Docker Manager';
		case 'check':
			return 'Checking the copy';
		case 'restart':
			return 'Restarting';
		case 'done':
			return 'Done';
	}
}

/** The status page's steps with the current one (a refused copy fails it). */
export function waitSteps(s: MoveStatus): WaitStep[] {
	const at = WAIT_ORDER.indexOf(waitStepOf(s));
	return WAIT_ORDER.map((id, i) => {
		let state: WaitStep['state'] = 'todo';
		if (i < at || s.phase === 'complete') state = 'done';
		else if (i === at) state = s.phase === 'failed' ? 'failed' : 'current';
		return { id, label: waitLabel(id, s, i === at), state };
	});
}

const WAIT_ERRORS: Record<NonNullable<MoveStatus['errorCode']>, string> = {
	manager_move_unreachable: 'The old server does not answer',
	manager_move_transfer_failed: 'The copy was interrupted',
	move_code_invalid: 'The pairing code does not match',
	move_clock_skew: "The two servers' clocks differ too much",
	manager_move_refused: 'The old server refused the move',
	manager_move_state_invalid: 'The copy cannot be used',
	manager_move_schema_incompatible: 'This Docker Manager is older than the old one',
	manager_move_not_handed_off: 'The old server has not handed over',
	manager_move_stage_failed: 'The copy could not be prepared'
};

export interface WaitNotice {
	tone: 'info' | 'warn' | 'danger';
	title: string;
	body: string;
}

/** What the status page says besides the steps: done, a problem, or what to do next. */
export function waitNotice(s: MoveStatus): WaitNotice | null {
	if (s.phase === 'complete') {
		const name = dnsName(s.publicUrl) ?? s.publicUrl;
		const point = name
			? `Point DNS for ${name} at this server, then sign in there as usual.`
			: 'Point your usual address at this server, then sign in there as usual.';
		return {
			tone: 'info',
			title: 'Docker Manager now runs here.',
			body: `${point} You can remove DOCKER_MANAGER_MOVE_FROM and DOCKER_MANAGER_MOVE_CODE from the .env.`
		};
	}
	if (s.phase === 'failed')
		return {
			tone: 'danger',
			title: (s.errorCode && WAIT_ERRORS[s.errorCode]) || 'The copy was refused',
			body: s.recovery || 'Fix the cause, then restart Docker Manager on this server.'
		};
	if (s.errorCode)
		return {
			tone: 'warn',
			title: WAIT_ERRORS[s.errorCode] ?? 'The move is held up',
			body: s.recovery || 'Docker Manager keeps trying.'
		};
	if (waitStepOf(s) === 'wait' && s.oldState === 'open')
		return {
			tone: 'info',
			title: 'Ready when you are',
			body: 'On the old server, press Move everything to start.'
		};
	return null;
}

// ---------------------------------------------------------------------------
// Move complete (the new manager, owner).

export interface CompleteItem {
	id: string;
	done: boolean;
	title: string;
	body?: string;
	/** A value to set by hand, shown as a copyable line. */
	fix?: string;
}

const REFUSED_CONFIRMATION =
	'The old server refused the confirmation. Make sure Docker Manager no longer runs there, then mark this as done.';

const CONFIRM_ERRORS: Record<string, string> = {
	unreachable: 'The old server does not answer. Docker Manager keeps trying.',
	clock_skew: "The two servers' clocks differ. Docker Manager keeps trying.",
	code_invalid: REFUSED_CONFIRMATION,
	state_refused: REFUSED_CONFIRMATION
};

function confirmErrorText(code: string): string {
	return (
		CONFIRM_ERRORS[code] ??
		'The old server answered with an error. Docker Manager keeps trying.'
	);
}

/** The old manager confirmed, or the owner marked it as done. */
export function oldManagerSettled(move: ManagerMove): boolean {
	return move.oldManagerConfirmed || !!move.confirmAcknowledgedAt;
}

/** "Mark as done" is offered once a confirmation failed. */
export function canAcknowledge(move: ManagerMove): boolean {
	return !oldManagerSettled(move) && !!move.confirmError;
}

/** An agent's fix in one line. */
export function redirectFix(r: MoveRedirect): string {
	return `DOCKER_AGENT_MANAGER_URL=${r.url}`;
}

/** What is left after the move, in the order to do it. */
export function completeItems(move: ManagerMove): CompleteItem[] {
	const settled = oldManagerSettled(move);
	const items: CompleteItem[] = [
		{
			id: 'confirm',
			done: settled,
			title: move.oldManagerConfirmed
				? 'The old server confirmed the move'
				: move.confirmAcknowledgedAt
					? 'Marked as done'
					: 'Waiting for the old server to confirm',
			body: !settled && move.confirmError ? confirmErrorText(move.confirmError) : undefined
		}
	];
	for (const r of move.redirects) {
		if (!r.needsFix) continue;
		const newServer = r.role === 'new_server';
		items.push({
			id: `agent:${r.environmentId}`,
			done: false,
			title: `${r.environmentName} did not get the new address`,
			body: newServer
				? `On ${r.environmentName}, remove the DOCKER_MANAGER_MOVE_FROM and DOCKER_MANAGER_MOVE_CODE lines from the .env, then run docker compose up -d.`
				: `On ${r.environmentName}, set this in the agent's settings (with DOCKER_AGENT_MANAGER_ALLOW_HTTP=true for an http address), then restart the agent.`,
			fix: newServer ? undefined : redirectFix(r)
		});
	}
	const old = move.oldEnvironment;
	if (old) {
		const copies = old.stoppedCopies;
		items.push({
			id: 'copies',
			done: copies === 0,
			title:
				copies === 0
					? `No old copies left on ${old.name}`
					: `Old copies are still on ${old.name}`,
			body:
				copies > 0
					? `${plural(copies, 'stopped copy', 'stopped copies')} of your moved apps. Remove ${copies === 1 ? 'it' : 'them'} once your apps work here.`
					: undefined
		});
		const left = old.stackCount;
		items.push({
			id: 'archive',
			done: old.archived,
			title: old.archived ? `${old.name} is archived` : `${old.name} is still active`,
			body: old.archived
				? undefined
				: left > 0
					? `${plural(left, 'stack', 'stacks')} did not move and ${left === 1 ? 'is' : 'are'} still on ${old.name}. Move or remove ${left === 1 ? 'it' : 'them'} first.`
					: 'Its apps run here now. Archive it once you no longer need it.'
		});
	}
	return items;
}

/** Everything after the move is done: the Move complete card goes away. */
export function moveCompleteDone(move: ManagerMove): boolean {
	return completeItems(move).every((i) => i.done);
}
