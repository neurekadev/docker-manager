// Tabs of the personal Profile: the caller's own account and API tokens.
// Nothing here is instance administration (that is Settings). Pure
// (profile.spec.ts).
import { routes } from '$lib/routes';
import type { TabLink } from '$lib/ui';

export function profileTabs(): TabLink[] {
	return [
		{ href: routes.profile(), label: 'Account' },
		{ href: routes.apiTokens(), label: 'API tokens' }
	];
}
