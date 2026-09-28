import { describe, expect, it } from 'vitest';
import { isValid, requiredErrors, submitted, untilFilled } from './validate';

describe('public form checks (#22 polish)', () => {
	it('names every empty required field, blank included', () => {
		const errors = requiredErrors({
			username: ['  ', 'Enter your username.'],
			password: ['secret', 'Enter your password.'],
			code: [undefined, 'Paste the invite link.']
		});
		expect(errors).toEqual({
			username: 'Enter your username.',
			code: 'Paste the invite link.'
		});
		expect(isValid(errors)).toBe(false);
		expect(isValid(requiredErrors({ a: ['x', 'A is missing.'] }))).toBe(true);
	});

	it('reads the submitted value first, so autofilled fields count', () => {
		const data = new FormData();
		data.set('username', 'ada');
		data.set('password', '');
		expect(submitted(data, 'username', '')).toBe('ada');
		expect(submitted(data, 'password', 'typed')).toBe('typed');
		expect(submitted(null, 'username', 'bound')).toBe('bound');
	});

	it('drops a missing-field message once the field is filled', () => {
		expect(untilFilled('Enter your username.', '')).toBe('Enter your username.');
		expect(untilFilled('Enter your username.', '  ')).toBe('Enter your username.');
		expect(untilFilled('Enter your username.', 'ada')).toBeUndefined();
		expect(untilFilled(undefined, '')).toBeUndefined();
	});
});
