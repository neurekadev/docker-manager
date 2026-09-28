import { describe, expect, it } from 'vitest';
import { createBlocker, type CreateDraft } from './creating';

const draft = (over: Partial<CreateDraft> = {}): CreateDraft => ({
	environment: { name: 'homelab', online: true },
	name: 'shop',
	compose: 'services:\n  web:\n    image: nginx\n',
	...over
});

describe('createBlocker', () => {
	it('is ready with an environment, a valid name and a Compose file', () => {
		expect(createBlocker(draft())).toBeUndefined();
	});

	it('names the first missing piece', () => {
		expect(createBlocker(draft({ environment: undefined, name: '' }))).toBe(
			'Choose an environment for the stack.'
		);
		expect(createBlocker(draft({ name: '  ' }))).toBe('Enter a name for the stack.');
		expect(createBlocker(draft({ name: 'My Shop' }))).toBe('Fix the name to continue.');
		expect(createBlocker(draft({ compose: '\n' }))).toBe('Add a Compose file.');
	});

	it('blocks creating, not validating, on an offline environment', () => {
		const offline = draft({ environment: { name: 'homelab', online: false } });
		expect(createBlocker(offline)).toBe(
			'homelab is offline. Create the stack when it is back.'
		);
		expect(createBlocker(offline, false)).toBeUndefined();
	});
});
