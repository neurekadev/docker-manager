// RFC 6238 TOTP from a known seed, for enrolling and passing TOTP in E2E
// tests (#16, #29). Uses node:crypto only.
import { createHmac } from 'node:crypto';

const BASE32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';

/** Decodes RFC 4648 base32 (case-insensitive, padding and spaces ignored). */
export function base32Decode(input: string): Buffer {
	const clean = input.toUpperCase().replace(/[\s=]/g, '');
	let bits = 0;
	let value = 0;
	const out: number[] = [];
	for (const ch of clean) {
		const idx = BASE32.indexOf(ch);
		if (idx < 0) throw new Error(`invalid base32 character ${JSON.stringify(ch)}`);
		value = (value << 5) | idx;
		bits += 5;
		if (bits >= 8) {
			out.push((value >>> (bits - 8)) & 0xff);
			bits -= 8;
		}
	}
	return Buffer.from(out);
}

export interface TotpOptions {
	/** Unix time in milliseconds (default Date.now()). */
	timeMs?: number;
	digits?: number;
	periodSeconds?: number;
	algorithm?: 'sha1' | 'sha256' | 'sha512';
}

/** HOTP (RFC 4226) for a raw key and counter. */
export function hotp(key: Buffer, counter: bigint, digits = 6, algorithm = 'sha1'): string {
	const msg = Buffer.alloc(8);
	msg.writeBigUInt64BE(counter);
	const mac = createHmac(algorithm, key).update(msg).digest();
	const offset = mac[mac.length - 1] & 0x0f;
	const code =
		((mac[offset] & 0x7f) << 24) |
		((mac[offset + 1] & 0xff) << 16) |
		((mac[offset + 2] & 0xff) << 8) |
		(mac[offset + 3] & 0xff);
	return (code % 10 ** digits).toString().padStart(digits, '0');
}

/** TOTP (RFC 6238) for a base32 seed, as authenticator apps compute it. */
export function totp(seedBase32: string, opts: TotpOptions = {}): string {
	const period = opts.periodSeconds ?? 30;
	const counter = BigInt(Math.floor((opts.timeMs ?? Date.now()) / 1000 / period));
	return hotp(base32Decode(seedBase32), counter, opts.digits ?? 6, opts.algorithm ?? 'sha1');
}

/**
 * Milliseconds until the current TOTP step ends; wait past it (with the
 * page clock, not a sleep) when a test must not reuse a code.
 */
export function msUntilNextStep(timeMs = Date.now(), periodSeconds = 30): number {
	const periodMs = periodSeconds * 1000;
	return periodMs - (timeMs % periodMs);
}
