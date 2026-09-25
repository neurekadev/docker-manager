import { describe, expect, it, vi } from 'vitest';
import { QueryClient } from '@tanstack/svelte-query';
import type { Session } from '$lib/api/client';
import { queryKeys } from '$lib/api/queries';
import { routes, safeNext } from '$lib/routes';
import { codeFromPasted, takeCodeFromFragment } from './code';
import { appDestination, publicDestination } from './guard';
import { qrPath } from './qr';
import { dropPrivateData, handleUnauthenticated, msUntilExpiryCheck } from './session';
import {
	b64urlToBuffer,
	bufferToB64url,
	creationOptions,
	credentialToJSON,
	requestOptions
} from './webauthn';

const session = (state: Session['state'], extra: Partial<Session> = {}): Session =>
	({ state, factors: [], missingFactors: [], requiredFactors: 'none', ...extra }) as Session;

describe('route guard', () => {
	it('sends visitors of app pages where they belong', () => {
		expect(appDestination('/stacks', session('authenticated'), true)).toBeNull();
		expect(appDestination('/stacks', null, false)).toBe('/setup');
		expect(appDestination('/stacks?x=1', null, true)).toBe('/sign-in?next=%2Fstacks%3Fx%3D1');
		expect(appDestination('/', null, undefined)).toBe('/sign-in');
		expect(appDestination('/jobs', session('second_factor_required'), true)).toBe(
			'/sign-in?next=%2Fjobs'
		);
		expect(appDestination('/jobs', session('enrollment_required'), true)).toBe('/enroll');
	});

	it('keeps public pages consistent with the session and setup state', () => {
		expect(publicDestination('setup', null, false)).toBeNull();
		expect(publicDestination('setup', null, true)).toBe('/sign-in');
		expect(publicDestination('setup', session('authenticated'), true)).toBe('/');
		expect(publicDestination('sign-in', null, true)).toBeNull();
		expect(publicDestination('sign-in', null, false)).toBe('/setup');
		expect(publicDestination('sign-in', session('authenticated'), true, '/stacks')).toBe(
			'/stacks'
		);
		expect(
			publicDestination('sign-in', session('authenticated'), true, 'https://evil.example/')
		).toBe('/');
		expect(publicDestination('sign-in', session('enrollment_required'), true)).toBe('/enroll');
		expect(publicDestination('sign-in', session('second_factor_required'), true)).toBeNull();
		expect(publicDestination('enroll', null, true)).toBe('/sign-in');
		expect(publicDestination('enroll', session('enrollment_required'), true)).toBeNull();
		expect(publicDestination('invitation', session('authenticated'), true)).toBeNull();
		expect(publicDestination('password-reset', null, true)).toBeNull();
	});

	it('never redirects to another origin', () => {
		expect(safeNext('//evil.example')).toBe('/');
		expect(safeNext('/\\evil.example')).toBe('/');
		expect(safeNext('https://evil.example')).toBe('/');
		expect(safeNext('/stacks/1')).toBe('/stacks/1');
		expect(routes.signIn('//evil')).toBe('/sign-in');
		expect(routes.signIn('/x', 'expired')).toBe('/sign-in?next=%2Fx&reason=expired');
	});
});

describe('session lifecycle', () => {
	it('re-checks at the first of idle or absolute expiry', () => {
		const now = Date.parse('2026-09-25T12:00:00Z');
		expect(msUntilExpiryCheck(null, now)).toBeNull();
		expect(msUntilExpiryCheck(session('authenticated'), now)).toBeNull();
		expect(
			msUntilExpiryCheck(
				session('authenticated', {
					idleExpiresAt: '2026-09-25T13:00:00Z',
					expiresAt: '2026-09-26T12:00:00Z'
				}),
				now
			)
		).toBe(3600_000 + 1000);
		expect(
			msUntilExpiryCheck(session('authenticated', { expiresAt: '2026-09-25T11:00:00Z' }), now)
		).toBe(0);
	});

	it('drops private data and goes to sign-in when a signed-in request answers 401', () => {
		const qc = new QueryClient();
		qc.setQueryData(queryKeys.session, session('authenticated'));
		qc.setQueryData(queryKeys.environments.list(), [{ id: 'e1' }]);
		qc.setQueryData(queryKeys.health, { status: 'ok' });
		const navigate = vi.fn();
		const resetShell = vi.fn();
		handleUnauthenticated({
			queryClient: qc,
			navigate,
			currentPath: () => '/stacks?x=1',
			resetShell
		});
		expect(navigate).toHaveBeenCalledWith('/sign-in?next=%2Fstacks%3Fx%3D1&reason=expired');
		expect(resetShell).toHaveBeenCalled();
		expect(qc.getQueryData(queryKeys.environments.list())).toBeUndefined();
		expect(qc.getQueryData(queryKeys.session)).toBeNull();
		expect(qc.getQueryData(queryKeys.health)).toEqual({ status: 'ok' });

		// Already signed out: nothing happens (the page shows sign-in itself).
		navigate.mockClear();
		handleUnauthenticated({ queryClient: qc, navigate, currentPath: () => '/' });
		expect(navigate).not.toHaveBeenCalled();
	});

	it('keeps only public setup and health data on sign-out', () => {
		const qc = new QueryClient();
		qc.setQueryData(queryKeys.setupStatus, { setupComplete: true });
		qc.setQueryData(queryKeys.myPermissions, { owner: true });
		dropPrivateData(qc);
		expect(qc.getQueryData(queryKeys.setupStatus)).toEqual({ setupComplete: true });
		expect(qc.getQueryData(queryKeys.myPermissions)).toBeUndefined();
	});
});

describe('one-time codes in the fragment', () => {
	it('reads the code and removes it from the address bar', () => {
		const replace = vi.fn();
		vi.stubGlobal('history', { state: null, replaceState: replace });
		expect(
			takeCodeFromFragment({ hash: '#code=dyi_abc123', pathname: '/invitation', search: '' })
		).toBe('dyi_abc123');
		expect(replace).toHaveBeenCalledWith(null, '', '/invitation');
		replace.mockClear();
		expect(takeCodeFromFragment({ hash: '', pathname: '/invitation', search: '' })).toBe('');
		expect(replace).not.toHaveBeenCalled();
		vi.unstubAllGlobals();
	});

	it('takes the code from a pasted link or a pasted code', () => {
		expect(codeFromPasted(' https://dy.example/invitation#code=dyi_abc123 ')).toBe(
			'dyi_abc123'
		);
		expect(codeFromPasted('dyi_abc123\n')).toBe('dyi_abc123');
		expect(codeFromPasted('https://dy.example/invitation#other=1')).toBe('');
		expect(codeFromPasted('')).toBe('');
	});
});

describe('WebAuthn JSON conversion', () => {
	it('round-trips base64url', () => {
		const bytes = new Uint8Array([0, 1, 2, 250, 251, 252, 253, 254, 255]);
		const s = bufferToB64url(bytes);
		expect(s).toBe('AAEC-vv8_f7_');
		expect(new Uint8Array(b64urlToBuffer(s))).toEqual(bytes);
	});

	it('decodes creation and request options from the manager JSON', () => {
		const c = creationOptions({
			publicKey: {
				challenge: 'AAEC',
				rp: { name: 'DockYard', id: 'localhost' },
				user: { id: 'AQID', name: 'admin', displayName: 'Admin' },
				pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
				excludeCredentials: [{ type: 'public-key', id: 'BAUG' }]
			}
		});
		expect(new Uint8Array(c.challenge as ArrayBuffer)).toEqual(new Uint8Array([0, 1, 2]));
		expect(new Uint8Array(c.user.id as ArrayBuffer)).toEqual(new Uint8Array([1, 2, 3]));
		expect(new Uint8Array(c.excludeCredentials![0].id as ArrayBuffer)).toEqual(
			new Uint8Array([4, 5, 6])
		);
		const r = requestOptions({
			publicKey: {
				challenge: 'AAEC',
				allowCredentials: [{ type: 'public-key', id: 'BAUG' }],
				rpId: 'localhost'
			}
		});
		expect(r.rpId).toBe('localhost');
		expect(new Uint8Array(r.allowCredentials![0].id as ArrayBuffer)).toEqual(
			new Uint8Array([4, 5, 6])
		);
		expect(
			requestOptions({ publicKey: { challenge: 'AAEC' } }).allowCredentials
		).toBeUndefined();
	});

	it('encodes an assertion when the browser has no toJSON()', () => {
		const buf = (...b: number[]) => new Uint8Array(b).buffer;
		const cred = {
			id: 'cred-1',
			rawId: buf(9, 9),
			type: 'public-key',
			authenticatorAttachment: 'platform',
			response: {
				clientDataJSON: buf(1),
				authenticatorData: buf(2),
				signature: buf(3),
				userHandle: buf(4)
			},
			getClientExtensionResults: () => ({})
		} as unknown as PublicKeyCredential;
		expect(credentialToJSON(cred)).toEqual({
			id: 'cred-1',
			rawId: 'CQk',
			type: 'public-key',
			authenticatorAttachment: 'platform',
			clientExtensionResults: {},
			response: {
				clientDataJSON: 'AQ',
				authenticatorData: 'Ag',
				signature: 'Aw',
				userHandle: 'BA'
			}
		});
	});
});

describe('QR codes for TOTP enrollment', () => {
	it('renders an otpauth URI as SVG path data with a quiet zone', () => {
		const q = qrPath('otpauth://totp/DockYard:admin?secret=JBSWY3DPEHPK3PXP&issuer=DockYard');
		expect(q.size).toBeGreaterThanOrEqual(21 + 6);
		expect((q.size - 6 - 21) % 4).toBe(0); // version sizes are 21 + 4n
		expect(q.path).toMatch(/^M3 3h1v1h-1z/); // finder pattern corner, offset by the border
		expect(q.path.split('M').length).toBeGreaterThan(100);
	});
});
