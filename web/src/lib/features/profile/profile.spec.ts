import { describe, expect, it } from 'vitest';
import { profileTabs } from './tabs';

describe('Profile tabs', () => {
	it('lead to my own account and API tokens only, outside Settings', () => {
		const tabs = profileTabs();
		expect(tabs.map((t) => [t.label, t.href])).toEqual([
			['Account', '/profile'],
			['API tokens', '/profile/tokens']
		]);
		expect(tabs.some((t) => t.href.startsWith('/settings'))).toBe(false);
	});
});
