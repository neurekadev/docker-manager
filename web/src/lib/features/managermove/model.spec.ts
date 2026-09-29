// Moving Docker Manager to a new server: the words of each move state and
// the locked manager's banner.
import { describe, expect, it } from 'vitest';
import {
	isLocked,
	isPolled,
	lockOf,
	moveBanner,
	moveStatus,
	thisAddress,
	type ManagerMove
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
			label: 'Waiting for the new server',
			tone: 'info',
			stop: 'cancel'
		});
		expect(moveStatus(move({ state: 'moving' }))).toMatchObject({
			label: 'Moving apps',
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
			label: 'Handed over',
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

	it('polls while the move changes on its own and locks from the handoff on', () => {
		expect(
			['open', 'moving', 'ready', 'draining', 'handed_off'].every((s) => isPolled(s as never))
		).toBe(true);
		expect(isPolled('confirmed')).toBe(false);
		expect(isPolled(undefined)).toBe(false);
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
			'This Docker Manager is moving to a new server'
		);
		expect(moveBanner('moving', false, address)?.body).toBe(
			'Nothing can be changed here. Your apps keep running.'
		);
		// A session read before the lock began: the first manager_moved refusal is enough.
		expect(moveBanner(undefined, true, address)?.title).toBe(
			'This Docker Manager is moving to a new server'
		);
		expect(moveBanner('none', true, address)?.title).toBe(
			'This Docker Manager is moving to a new server'
		);
	});

	it('names the address once the move is confirmed', () => {
		expect(moveBanner('moved', true, address)).toEqual({
			title: 'This Docker Manager moved to a new server',
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
