// Moving Docker Manager to a new server: the words of each move state, the
// locked manager's banner, the old manager's wizard, the new server's
// status page and Move complete.
import { describe, expect, it } from 'vitest';
import {
	ACTIVE_STATES,
	AGENT_KEPT,
	BEFORE_YOU_START,
	MOVE_STEPS,
	NOT_ASKING,
	ONLY_MANAGER_MOVES,
	SETUP_FILES_EXPIRED,
	cancelConsequences,
	heredocDelimiter,
	restartsWhenEnded,
	setupFilesConsequences,
	setupFilesNotice,
	setupFilesReason,
	setupScript,
	canAcknowledge,
	canMoveEverything,
	completeItems,
	dnsName,
	hostOnly,
	isActive,
	isHandedOver,
	isLocked,
	isWaitingPhase,
	lockOf,
	moveBanner,
	moveButtonLabel,
	moveCompleteDone,
	moveStatus,
	movedPanel,
	newServerChecklist,
	newServerReady,
	nothingToMove,
	onlyManagerMoves,
	runView,
	stepOf,
	thisAddress,
	waitNotice,
	waitStepOf,
	waitSteps,
	type ManagerMove,
	type MoveStatus
} from './model';

function move(p: Partial<ManagerMove> = {}): ManagerMove {
	return {
		id: 'mv-1',
		state: 'open',
		createdAt: '2026-09-29T10:00:00Z',
		expiresAt: '2026-10-06T10:00:00Z',
		jobsRunning: 0,
		oldManagerConfirmed: false,
		confirmAttempts: 0,
		redirects: [],
		...p
	};
}

describe('move status on the old manager', () => {
	it('says what each state means and which way out it offers', () => {
		expect(moveStatus(move())).toMatchObject({
			label: 'Waiting for the New Server',
			tone: 'info',
			stop: 'cancel'
		});
		expect(moveStatus(move({ state: 'moving' }))).toMatchObject({
			label: 'Moving Apps',
			stop: 'cancel'
		});
		expect(moveStatus(move({ state: 'ready' })).stop).toBe('cancel');
		expect(moveStatus(move({ state: 'draining', jobsRunning: 3 }))).toMatchObject({
			label: 'Locked',
			title: 'Locked: finishing 3 running jobs',
			stop: 'cancel'
		});
		expect(moveStatus(move({ state: 'draining', jobsRunning: 1 })).title).toBe(
			'Locked: finishing 1 running job'
		);
		expect(moveStatus(move({ state: 'draining' })).title).toBe('Locked: handing over');
		expect(moveStatus(move({ state: 'handed_off' }))).toMatchObject({
			label: 'Handed Over',
			title: 'Handed over: waiting for the new Docker Manager to confirm',
			stop: 'resume'
		});
		expect(moveStatus(move({ state: 'confirmed' }))).toMatchObject({
			label: 'Moved',
			tone: 'ok',
			stop: null
		});
		expect(moveStatus(move({ state: 'expired' })).stop).toBeNull();
		expect(moveStatus(move({ state: 'cancelled' })).stop).toBeNull();
		expect(moveStatus(move({ state: 'arrived' })).label).toBe('Arrived');
	});

	it('knows a move under way and locks from the handoff on', () => {
		expect(ACTIVE_STATES).toEqual(['open', 'moving', 'ready', 'draining', 'handed_off']);
		expect(isLocked('open')).toBe(false);
		expect(isLocked('moving')).toBe(false);
		expect(['draining', 'handed_off', 'confirmed'].every((s) => isLocked(s as never))).toBe(
			true
		);
		expect(isLocked('cancelled')).toBe(false);
	});
});

describe('the banner of a locked manager', () => {
	const address = 'https://docker.example.com';

	it('shows while the manager moves, from the session lock or a refused change', () => {
		expect(moveBanner('moving', false, address)?.title).toBe(
			'This Docker Manager Is Moving to a New Server'
		);
		expect(moveBanner('moving', false, address)?.body).toBe(
			'Nothing can be changed here. Your apps keep running.'
		);
		// A session read before the lock began: the first manager_moved refusal is enough.
		expect(moveBanner(undefined, true, address)?.title).toBe(
			'This Docker Manager Is Moving to a New Server'
		);
		expect(moveBanner('none', true, address)?.title).toBe(
			'This Docker Manager Is Moving to a New Server'
		);
	});

	it('names the address once the move is confirmed', () => {
		expect(moveBanner('moved', true, address)).toEqual({
			title: 'This Docker Manager Moved to a New Server',
			body: 'Nothing can be changed here. Use https://docker.example.com once it leads to the new server.'
		});
	});

	it('stays away while nothing is locked', () => {
		for (const l of ['none', undefined] as const)
			expect(moveBanner(l, false, address)).toBeNull();
	});

	it('reads the lock from the owner move state', () => {
		expect(lockOf('draining')).toBe('moving');
		expect(lockOf('handed_off')).toBe('moving');
		expect(lockOf('confirmed')).toBe('moved');
		for (const s of ['open', 'moving', 'ready', 'cancelled', 'expired', 'arrived'] as const)
			expect(lockOf(s)).toBe('none');
		expect(lockOf(undefined)).toBeUndefined();
	});

	it('uses the public URL, else the browser address', () => {
		expect(thisAddress('https://dm.example.org', 'https://other')).toBe(
			'https://dm.example.org'
		);
		expect(thisAddress('', 'https://seen.example')).toBe('https://seen.example');
		expect(thisAddress(undefined, undefined)).toBe('https://docker.example.com');
	});
});

describe('the old manager’s wizard', () => {
	const newServer = (online: boolean, managerCheckedIn: boolean, enrollmentState = 'used') => ({
		newServer: {
			online,
			managerCheckedIn,
			environmentId: 'env-new',
			environmentName: 'new-box',
			enrollmentState: enrollmentState as 'used'
		}
	});

	it('opens where the move stands', () => {
		expect(stepOf(null)).toBe('server');
		expect(stepOf(undefined)).toBe('server');
		expect(stepOf(move())).toBe('server');
		// A run that stopped: back on the Move step with its reason.
		expect(stepOf(move({ progress: { jobId: 'j', stacksMoved: 0, stacksTotal: 2 } }))).toBe(
			'move'
		);
		for (const state of ['moving', 'ready', 'draining', 'handed_off'] as const)
			expect(stepOf(move({ state }))).toBe('move');
		for (const state of ['cancelled', 'expired', 'arrived', 'confirmed'] as const)
			expect(stepOf(move({ state }))).toBe('server');
		expect(MOVE_STEPS.map((s) => s.label)).toEqual(['New Server', 'Check', 'Move']);
	});

	it('knows an active and a handed-over move', () => {
		expect(isActive(move())).toBe(true);
		expect(isActive(move({ state: 'handed_off' }))).toBe(true);
		expect(isActive(move({ state: 'confirmed' }))).toBe(false);
		expect(isActive(move({ state: 'cancelled' }))).toBe(false);
		expect(isActive(null)).toBe(false);
		expect(isHandedOver(move({ state: 'handed_off' }))).toBe(true);
		expect(isHandedOver(move({ state: 'confirmed' }))).toBe(true);
		expect(isHandedOver(move({ state: 'draining' }))).toBe(false);
	});

	it('checks off the new server: its agent, then its Docker Manager', () => {
		expect(newServerChecklist(move())).toEqual([
			{ label: "New server's agent connected", done: false, detail: undefined },
			{ label: 'New Docker Manager is waiting', done: false }
		]);
		expect(newServerReady(move())).toBe(false);
		expect(newServerReady(move(newServer(true, false)))).toBe(false);
		expect(newServerReady(move(newServer(true, true)))).toBe(true);
		expect(newServerReady(null)).toBe(false);
		expect(newServerChecklist(move(newServer(false, false, 'expired')))[0].detail).toBe(
			SETUP_FILES_EXPIRED
		);
		expect(SETUP_FILES_EXPIRED).toBe('The setup files expired. Create new setup files.');
	});

	it('offers new setup files while the new server is not ready and the files are gone or expired', () => {
		const waiting = move({ newServer: { online: false, managerCheckedIn: false } });
		expect(setupFilesReason(waiting, false)).toBe('not_shown');
		expect(setupFilesReason(waiting, true)).toBeNull();
		const expired = move(newServer(false, false, 'expired'));
		expect(setupFilesReason(expired, true)).toBe('expired');
		expect(setupFilesReason(move(newServer(false, false, 'revoked')), false)).toBe('expired');
		// Ready, or past the New server step: nothing to offer.
		expect(setupFilesReason(move(newServer(true, true)), false)).toBeNull();
		expect(
			setupFilesReason(
				move({ ...newServer(false, false, 'expired'), state: 'moving' }),
				false
			)
		).toBeNull();
		expect(setupFilesNotice('expired').title).toBe('The setup files expired.');
		expect(setupFilesNotice('not_shown')).toEqual({
			title: 'The setup files were shown when you created the move.',
			body: 'If you no longer have them, create new ones. The old files then stop working.'
		});
	});

	it('says what new setup files do, keeping an agent that already connected', () => {
		const fresh = setupFilesConsequences(
			move({ newServer: { online: false, managerCheckedIn: false } })
		);
		expect(fresh[0]).toBe(
			'The setup files you have now stop working. Docker Manager on the new server must use the new ones.'
		);
		expect(fresh[1]).toBe(
			"The new server's agent gets a new enrollment token, valid for 24 hours."
		);
		expect(setupFilesConsequences(move(newServer(true, false)))[1]).toBe(
			'The agent on the new server stays connected.'
		);
		expect(AGENT_KEPT).toContain('no enrollment token');
	});

	it('writes both files and starts Docker Manager in one paste', () => {
		const script = setupScript({
			composeYaml: 'name: docker-manager\nservices: {}\n',
			env: 'DOCKER_MANAGER_MOVE_CODE=dmm_a_$HOME`x`\\n\nDOCKER_AGENT_ENROLLMENT_TOKEN=dye_t\n'
		});
		expect(script.split('\n')).toEqual([
			'(',
			'set -e',
			'mkdir -p docker-manager',
			'cd docker-manager',
			"cat > compose.yaml <<'DOCKER_MANAGER_EOF'",
			'name: docker-manager',
			'services: {}',
			'DOCKER_MANAGER_EOF',
			'touch .env',
			'chmod 600 .env',
			"cat > .env <<'DOCKER_MANAGER_EOF'",
			// Quoted delimiters: $, backticks and backslashes stay literal.
			'DOCKER_MANAGER_MOVE_CODE=dmm_a_$HOME`x`\\n',
			'DOCKER_AGENT_ENROLLMENT_TOKEN=dye_t',
			'DOCKER_MANAGER_EOF',
			'docker compose up -d',
			')'
		]);
		// A file without a final newline still ends before its delimiter.
		expect(setupScript({ composeYaml: 'a', env: '' }, 'dm').split('\n')).toEqual([
			'(',
			'set -e',
			'mkdir -p dm',
			'cd dm',
			"cat > compose.yaml <<'DOCKER_MANAGER_EOF'",
			'a',
			'DOCKER_MANAGER_EOF',
			'touch .env',
			'chmod 600 .env',
			"cat > .env <<'DOCKER_MANAGER_EOF'",
			'',
			'DOCKER_MANAGER_EOF',
			'docker compose up -d',
			')'
		]);
	});

	it('picks a here-document delimiter that is no line of the file', () => {
		expect(heredocDelimiter('a\nb\n')).toBe('DOCKER_MANAGER_EOF');
		expect(heredocDelimiter('x\nDOCKER_MANAGER_EOF\n')).toBe('DOCKER_MANAGER_EOF_2');
		expect(heredocDelimiter('DOCKER_MANAGER_EOF\r\nDOCKER_MANAGER_EOF_2\n')).toBe(
			'DOCKER_MANAGER_EOF_3'
		);
		// Only whole lines end a here-document.
		expect(heredocDelimiter('  DOCKER_MANAGER_EOF\nDOCKER_MANAGER_EOF=1')).toBe(
			'DOCKER_MANAGER_EOF'
		);
		const tricky = setupScript({ composeYaml: 'DOCKER_MANAGER_EOF\n', env: 'A=1\n' });
		expect(tricky).toContain(
			"cat > compose.yaml <<'DOCKER_MANAGER_EOF_2'\nDOCKER_MANAGER_EOF\nDOCKER_MANAGER_EOF_2\n"
		);
		expect(tricky).toContain("cat > .env <<'DOCKER_MANAGER_EOF'\nA=1\nDOCKER_MANAGER_EOF\n");
	});

	it('knows when ending the move restarts Docker Manager, and says so', () => {
		const sent = {
			environmentId: 'e',
			environmentName: 'old',
			role: 'old_server' as const,
			url: 'http://x',
			sent: true,
			connected: false,
			needsFix: false
		};
		expect(restartsWhenEnded(move())).toBe(false);
		expect(restartsWhenEnded(move({ state: 'handed_off' }))).toBe(true);
		expect(
			restartsWhenEnded(move({ state: 'draining', redirects: [{ ...sent, sent: false }] }))
		).toBe(false);
		expect(restartsWhenEnded(move({ state: 'draining', redirects: [sent] }))).toBe(true);
		expect(cancelConsequences(move())).toHaveLength(3);
		expect(cancelConsequences(move({ state: 'draining', redirects: [sent] })).at(-1)).toBe(
			'Docker Manager restarts, because its agents already heard the new address.'
		);
		expect(cancelConsequences(null)).toHaveLength(3);
	});

	it('names hosts without their port', () => {
		expect(hostOnly('192.168.1.20:8080')).toBe('192.168.1.20');
		expect(hostOnly('nas.lan')).toBe('nas.lan');
		expect(hostOnly('[fd00::1]:8080')).toBe('fd00::1');
		expect(hostOnly('fd00::1')).toBe('fd00::1');
		expect(hostOnly(undefined)).toBe('');
		expect(dnsName('https://docker.example.com')).toBe('docker.example.com');
		expect(dnsName('')).toBeNull();
		expect(dnsName('not a url')).toBeNull();
	});

	it('lets Move everything start when nothing moves or the check allows it', () => {
		const source = { environmentId: 'env-1', name: 'old-box', online: true, stackCount: 3 };
		const ok = { allowed: true, stacks: [{}], blockers: [] };
		const blocked = { allowed: false, stacks: [{}], blockers: [{ code: 'port_conflict' }] };
		const none = { allowed: false, stacks: [], blockers: [{ code: 'no_stacks' }] };
		// No environment next to Docker Manager, or no stacks there: only Docker Manager moves.
		expect(onlyManagerMoves(move(), null)).toBe(true);
		expect(canMoveEverything(move(), null)).toBe(true);
		expect(
			canMoveEverything(move({ sourceEnvironment: { ...source, stackCount: 0 } }), null)
		).toBe(true);
		const withApps = move({ sourceEnvironment: source });
		expect(canMoveEverything(withApps, null)).toBe(false);
		expect(canMoveEverything(withApps, ok)).toBe(true);
		expect(canMoveEverything(withApps, blocked)).toBe(false);
		expect(nothingToMove(none)).toBe(true);
		expect(onlyManagerMoves(withApps, none)).toBe(true);
		expect(canMoveEverything(withApps, none)).toBe(true);
		expect(BEFORE_YOU_START).toEqual([
			'Your usual address stops working while your reverse proxy moves, until you point DNS at the new server.',
			"If your reverse proxy sends Docker Manager's address to this server's IP, change it to the new server's IP."
		]);
		expect(ONLY_MANAGER_MOVES).toBe(
			'Only Docker Manager moves: there are no apps on this server to move.'
		);
	});

	it('shows Move everything from the move alone', () => {
		expect(runView(move())).toMatchObject({ phase: 'idle' });
		expect(moveButtonLabel('idle')).toBe('Move Everything');
		const moving = runView(
			move({
				state: 'moving',
				progress: { jobId: 'j', stacksMoved: 2, stacksTotal: 5, currentStack: 'nextcloud' }
			})
		);
		expect(moving).toEqual({
			phase: 'moving',
			title: 'Moving your apps: 2 of 5 stacks',
			detail: 'Now moving nextcloud.',
			moved: 2,
			total: 5
		});
		expect(moveButtonLabel('moving')).toBe('Moving…');
		// The move reads open until the job's first step: a running job is moving.
		expect(
			runView(
				move({
					progress: { jobId: 'j', jobState: 'running', stacksMoved: 0, stacksTotal: 0 }
				})
			)
		).toMatchObject({ phase: 'moving', title: 'Moving your apps…' });
		expect(runView(move({ state: 'ready' }))).toMatchObject({
			phase: 'handing_over',
			title: 'Handing over Docker Manager…',
			detail: undefined
		});
		// Ready, but the new Docker Manager stopped asking (announced live).
		expect(
			runView(move({ state: 'ready', newServer: { online: true, managerCheckedIn: false } }))
				.detail
		).toBe(NOT_ASKING);
		expect(runView(move({ state: 'draining', jobsRunning: 2 })).detail).toBe(
			'Waiting for 2 running jobs to finish.'
		);
		expect(runView(move({ state: 'handed_off' }))).toMatchObject({
			phase: 'handed_over',
			title: 'Handed over'
		});
		expect(runView(move({ state: 'confirmed' })).phase).toBe('moved');
	});

	it('says why a run stopped and offers to try again', () => {
		const failed = runView(
			move({
				progress: {
					jobId: 'j',
					jobState: 'failed',
					errorCode: 'manager_move_apps_not_moved',
					recovery: 'nextcloud did not start on new-box. Press Move everything again.',
					stacksMoved: 1,
					stacksTotal: 3
				}
			})
		);
		expect(failed).toMatchObject({
			phase: 'failed',
			title: 'Not every app moved',
			recovery: 'nextcloud did not start on new-box. Press Move everything again.'
		});
		expect(moveButtonLabel('failed')).toBe('Try Again');
		expect(
			runView(
				move({
					progress: { jobId: 'j', jobState: 'cancelled', stacksMoved: 0, stacksTotal: 1 }
				})
			)
		).toMatchObject({
			phase: 'failed',
			title: 'The move stopped',
			recovery: 'Fix the cause, then try again.'
		});
	});

	it('says where to point DNS once handed over', () => {
		const m = move({
			state: 'handed_off',
			newServerAddress: '192.168.1.20:8080',
			statusUrl: 'http://192.168.1.20:8080'
		});
		expect(movedPanel(m, 'https://docker.example.com')).toEqual({
			title: 'Docker Manager moved.',
			body: 'Point DNS for docker.example.com at the new server (192.168.1.20).',
			statusUrl: 'http://192.168.1.20:8080'
		});
		expect(movedPanel(m, '').body).toBe(
			'Open Docker Manager on the new server (192.168.1.20) from now on.'
		);
	});
});

describe('the new server’s status page', () => {
	function status(p: Partial<MoveStatus> = {}): MoveStatus {
		return {
			phase: 'waiting',
			stacksMoved: 0,
			stacksTotal: 0,
			jobsRunning: 0,
			bytesReceived: 0,
			bytesTotal: 0,
			oldManagerConfirmed: false,
			...p
		};
	}
	const current = (s: MoveStatus) =>
		waitSteps(s).find((x) => x.state !== 'done' && x.state !== 'todo');

	it('shows the status page in every waiting phase, not before or after', () => {
		expect(isWaitingPhase('none')).toBe(false);
		expect(isWaitingPhase('complete')).toBe(false);
		expect(isWaitingPhase(undefined)).toBe(false);
		for (const p of ['connecting', 'waiting', 'copying', 'restarting', 'failed'] as const)
			expect(isWaitingPhase(p)).toBe(true);
	});

	it('highlights the step the move is at', () => {
		expect(waitSteps(status({ oldState: 'open' })).map((s) => s.label)).toEqual([
			'Waiting for the Old Server',
			'Moving Your Apps',
			'Finishing Running Jobs',
			'Copying Docker Manager',
			'Checking the Copy',
			'Restarting',
			'Done'
		]);
		expect(current(status({ phase: 'connecting' }))).toMatchObject({
			id: 'wait',
			state: 'current'
		});
		expect(
			current(
				status({
					oldState: 'moving',
					stacksMoved: 2,
					stacksTotal: 5,
					currentStack: 'immich'
				})
			)
		).toEqual({
			id: 'apps',
			label: 'Moving Your Apps (2 of 5, now: immich)',
			state: 'current'
		});
		expect(waitStepOf(status({ oldState: 'ready' }))).toBe('jobs');
		expect(waitStepOf(status({ phase: 'finishing_jobs' }))).toBe('jobs');
		expect(
			current(
				status({
					phase: 'copying',
					bytesReceived: 512 * 1024 * 1024,
					bytesTotal: 2 * 1024 ** 3
				})
			)
		).toMatchObject({ id: 'copy', label: 'Copying Docker Manager (512 MB of 2 GB)' });
		expect(waitStepOf(status({ phase: 'staging' }))).toBe('check');
		expect(current(status({ phase: 'restarting' }))?.label).toBe('Restarting');
		// Earlier steps are done; the apps keep their count.
		const copying = waitSteps(status({ phase: 'copying', stacksMoved: 5, stacksTotal: 5 }));
		expect(copying.slice(0, 3).every((s) => s.state === 'done')).toBe(true);
		expect(copying[1].label).toBe('Moving Your Apps (5 of 5)');
		expect(copying[4].state).toBe('todo');
	});

	it('is done when complete and says where to point DNS', () => {
		const done = status({ phase: 'complete', publicUrl: 'https://docker.example.com' });
		expect(waitSteps(done).every((s) => s.state === 'done')).toBe(true);
		expect(waitNotice(done)).toEqual({
			tone: 'info',
			title: 'Docker Manager now runs here.',
			body: 'Point DNS for docker.example.com at this server, then sign in there as usual. You can remove DOCKER_MANAGER_MOVE_FROM and DOCKER_MANAGER_MOVE_CODE from the .env.'
		});
		expect(waitNotice(status({ phase: 'complete' }))?.body).toMatch(
			/^Point your usual address at this server/
		);
	});

	it('shows a refused copy as failed with its recovery', () => {
		const failed = status({
			phase: 'failed',
			errorCode: 'manager_move_schema_incompatible',
			recovery: 'Update Docker Manager on this server, then restart it.'
		});
		expect(current(failed)).toMatchObject({ id: 'check', state: 'failed' });
		expect(waitNotice(failed)).toEqual({
			tone: 'danger',
			title: 'This Docker Manager is older than the old one',
			body: 'Update Docker Manager on this server, then restart it.'
		});
	});

	it('warns while it keeps trying, and says what to do next', () => {
		expect(
			waitNotice(
				status({
					phase: 'connecting',
					errorCode: 'manager_move_unreachable',
					recovery: 'Check that the old server is running.'
				})
			)
		).toEqual({
			tone: 'warn',
			title: 'The old server does not answer',
			body: 'Check that the old server is running.'
		});
		expect(waitNotice(status({ oldState: 'open' }))).toEqual({
			tone: 'info',
			title: 'Ready When You Are',
			body: 'On the old server, press Move Everything to start.'
		});
		expect(waitNotice(status({ oldState: 'moving' }))).toBeNull();
	});
});

describe('Move complete', () => {
	const arrived = (p: Partial<ManagerMove> = {}) =>
		move({ state: 'arrived', sourceUrl: 'http://192.168.1.10:8080', ...p });
	const old = {
		environmentId: 'env-old',
		name: 'old-box',
		online: true,
		archived: false,
		stackCount: 0,
		stoppedCopies: 3,
		migrationId: 'job-e'
	};

	it('waits for the old server, then counts it as done', () => {
		expect(completeItems(arrived())[0]).toMatchObject({
			id: 'confirm',
			done: false,
			title: 'Waiting for the old server to confirm',
			body: undefined
		});
		expect(canAcknowledge(arrived())).toBe(false);
		const refused = arrived({ confirmError: 'state_refused' });
		expect(canAcknowledge(refused)).toBe(true);
		expect(completeItems(refused)[0].body).toBe(
			'The old server refused the confirmation. Make sure Docker Manager no longer runs there, then mark this as done.'
		);
		expect(completeItems(arrived({ confirmError: 'http_502' }))[0].body).toBe(
			'The old server answered with an error. Docker Manager keeps trying.'
		);
		expect(completeItems(arrived({ oldManagerConfirmed: true }))[0]).toMatchObject({
			done: true,
			title: 'The old server confirmed the move'
		});
		const acked = arrived({
			confirmError: 'unreachable',
			confirmAcknowledgedAt: '2026-09-29T12:00:00Z'
		});
		expect(completeItems(acked)[0]).toMatchObject({ done: true, title: 'Marked as done' });
		expect(canAcknowledge(acked)).toBe(false);
	});

	it('lists agents that did not get the new address with their fix', () => {
		const items = completeItems(
			arrived({
				oldManagerConfirmed: true,
				redirects: [
					{
						environmentId: 'env-old',
						environmentName: 'old-box',
						role: 'old_server',
						url: 'http://192.168.1.20:8080',
						sent: false,
						errorCode: 'offline',
						connected: false,
						needsFix: true
					},
					{
						environmentId: 'env-new',
						environmentName: 'new-box',
						role: 'new_server',
						url: 'http://docker-manager:8080',
						sent: false,
						connected: false,
						needsFix: true
					},
					{
						environmentId: 'env-ok',
						environmentName: 'fine',
						role: 'old_server',
						url: 'http://x',
						sent: true,
						connected: true,
						needsFix: false
					}
				]
			})
		);
		expect(items.map((i) => i.id)).toEqual(['confirm', 'agent:env-old', 'agent:env-new']);
		expect(items[1]).toMatchObject({
			title: 'old-box did not get the new address',
			fix: 'DOCKER_AGENT_MANAGER_URL=http://192.168.1.20:8080'
		});
		expect(items[2].fix).toBeUndefined();
		expect(items[2].body).toMatch(/remove the DOCKER_MANAGER_MOVE_FROM/);
	});

	it('removes the old copies, then archives the old environment', () => {
		const items = completeItems(arrived({ oldManagerConfirmed: true, oldEnvironment: old }));
		expect(items.slice(1)).toEqual([
			{
				id: 'copies',
				done: false,
				title: 'Old copies are still on old-box',
				body: '3 stopped copies of your moved apps. Remove them once your apps work here.'
			},
			{
				id: 'archive',
				done: false,
				title: 'old-box is still active',
				body: 'Its apps run here now. Archive it once you no longer need it.'
			}
		]);
		expect(completeItems(arrived({ oldEnvironment: { ...old, stackCount: 1 } }))[2].body).toBe(
			'1 stack did not move and is still on old-box. Move or remove it first.'
		);
		expect(moveCompleteDone(arrived({ oldManagerConfirmed: true, oldEnvironment: old }))).toBe(
			false
		);
		expect(
			moveCompleteDone(
				arrived({
					oldManagerConfirmed: true,
					oldEnvironment: { ...old, stoppedCopies: 0, archived: true }
				})
			)
		).toBe(true);
		expect(moveCompleteDone(arrived({ oldManagerConfirmed: true }))).toBe(true);
	});
});
