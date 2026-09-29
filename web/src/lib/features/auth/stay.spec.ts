import { describe, expect, it } from 'vitest';
import type { StorageLike } from '$lib/shell/environment.svelte';
import { STAY_SIGNED_IN_KEY, readStaySignedIn, rememberStaySignedIn } from './stay';

function memory(): StorageLike & { data: Map<string, string> } {
	const data = new Map<string, string>();
	return {
		data,
		getItem: (k) => data.get(k) ?? null,
		setItem: (k, v) => void data.set(k, v),
		removeItem: (k) => void data.delete(k)
	};
}

const refusing: StorageLike = {
	getItem: () => {
		throw new Error('SecurityError');
	},
	setItem: () => {
		throw new Error('QuotaExceededError');
	},
	removeItem: () => {}
};

describe('Stay signed in choice (#16)', () => {
	it('is off until chosen, then remembers the last choice', () => {
		const s = memory();
		expect(readStaySignedIn(s)).toBe(false);
		rememberStaySignedIn(true, s);
		expect(s.data.get(STAY_SIGNED_IN_KEY)).toBe('1');
		expect(readStaySignedIn(s)).toBe(true);
		rememberStaySignedIn(false, s);
		expect(readStaySignedIn(s)).toBe(false);
		expect(STAY_SIGNED_IN_KEY).toBe('docker-manager:stay-signed-in');
	});

	it('works without storage or when storage refuses', () => {
		expect(readStaySignedIn(null)).toBe(false);
		expect(() => rememberStaySignedIn(true, null)).not.toThrow();
		expect(readStaySignedIn(refusing)).toBe(false);
		expect(() => rememberStaySignedIn(true, refusing)).not.toThrow();
	});

	it('ignores values it did not write', () => {
		const s = memory();
		s.setItem(STAY_SIGNED_IN_KEY, 'yes');
		expect(readStaySignedIn(s)).toBe(false);
	});
});
