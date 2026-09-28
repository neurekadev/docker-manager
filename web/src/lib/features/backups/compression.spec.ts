import { describe, expect, it } from 'vitest';
import { COMPRESSION_OPTIONS, compressionText } from './model';

describe('repository compression', () => {
	it('offers the modes the API accepts, Automatic first', () => {
		expect(COMPRESSION_OPTIONS.map((o) => o.value)).toEqual(['auto', 'max', 'off']);
		expect(COMPRESSION_OPTIONS.map((o) => o.label)).toEqual(['Automatic', 'Maximum', 'Off']);
	});

	it('names a mode in words; a missing one is Automatic', () => {
		expect(compressionText('max')).toBe('Maximum');
		expect(compressionText('off')).toBe('Off');
		expect(compressionText('auto')).toBe('Automatic');
		expect(compressionText(undefined)).toBe('Automatic');
	});
});
