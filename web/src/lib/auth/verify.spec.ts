import { describe, expect, it } from 'vitest';
import type { StorageLike } from '$lib/shell/environment.svelte';
import {
	VERIFY_WITH_KEY,
	initialMethod,
	rememberMethod,
	secondFactorMethods,
	stepUpMethods
} from './verify';

function memory(init: Record<string, string> = {}): StorageLike & { data: Record<string, string> } {
	const data = { ...init };
	return {
		data,
		getItem: (k) => data[k] ?? null,
		setItem: (k, v) => void (data[k] = v),
		removeItem: (k) => void delete data[k]
	};
}

describe('verification method (#186)', () => {
	it('confirms a step-up with exactly one factor, a passkey first', () => {
		const f = (password: boolean, totp: boolean, passkeys: number) => ({
			password,
			totp,
			passkeys
		});
		expect(stepUpMethods(f(true, false, 0), true)).toEqual(['password']);
		expect(stepUpMethods(f(true, true, 0), true)).toEqual(['totp']);
		expect(stepUpMethods(f(true, false, 2), true)).toEqual(['passkey']);
		expect(stepUpMethods(f(false, false, 1), true)).toEqual(['passkey']);
		expect(stepUpMethods(f(true, true, 1), true)).toEqual(['passkey', 'totp']);
		// Never the password once a second factor exists, even when this
		// browser can't use passkeys.
		expect(stepUpMethods(f(true, false, 1), false)).toEqual([]);
		expect(stepUpMethods(f(true, true, 1), false)).toEqual(['totp']);
	});

	it('orders the factors of a pending sign-in the same way', () => {
		expect(secondFactorMethods(['totp', 'passkey', 'recovery_code'], true)).toEqual([
			'passkey',
			'totp'
		]);
		expect(secondFactorMethods(['totp', 'passkey'], false)).toEqual(['totp']);
		expect(secondFactorMethods(['passkey'], true)).toEqual(['passkey']);
	});

	it('starts with the remembered choice when the account has it', () => {
		expect(initialMethod(['passkey', 'totp'], memory())).toBe('passkey');
		expect(initialMethod(['passkey', 'totp'], memory({ [VERIFY_WITH_KEY]: 'totp' }))).toBe(
			'totp'
		);
		expect(initialMethod(['passkey'], memory({ [VERIFY_WITH_KEY]: 'totp' }))).toBe('passkey');
		expect(initialMethod([], memory())).toBeNull();
		expect(initialMethod(['totp'], null)).toBe('totp');
	});

	it('remembers a switch between passkey and code, never the password', () => {
		const s = memory();
		rememberMethod('totp', s);
		expect(s.data[VERIFY_WITH_KEY]).toBe('totp');
		rememberMethod('passkey', s);
		expect(s.data[VERIFY_WITH_KEY]).toBe('passkey');
		rememberMethod('password', s);
		expect(s.data[VERIFY_WITH_KEY]).toBe('passkey');
		const refusing: StorageLike = {
			getItem: () => {
				throw new Error('denied');
			},
			setItem: () => {
				throw new Error('denied');
			},
			removeItem: () => {}
		};
		expect(() => rememberMethod('totp', refusing)).not.toThrow();
		expect(initialMethod(['passkey', 'totp'], refusing)).toBe('passkey');
	});
});
