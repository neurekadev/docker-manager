// Tabs of the Settings section: instance administration only (the
// caller's own account and API tokens are the Profile). Tabs the caller
// can't use are hidden, not disabled; the server still decides. Pure
// (settings.spec.ts).
import { routes } from '$lib/routes';
import type { Access } from '$lib/shell/nav';
import type { TabLink } from '$lib/ui';
import { can } from '$lib/features/common/access';

export function settingsTabs(access: Access): TabLink[] {
	const t: TabLink[] = [{ href: routes.settings(), label: 'Overview' }];
	if (access.owner) t.push({ href: routes.allApiTokens(), label: 'API tokens' });
	if (access.owner) t.push({ href: routes.signInPolicy(), label: 'Sign-in policy' });
	if (can(access, 'settings.read'))
		t.push({ href: routes.scheduleDefaults(), label: 'Schedule defaults' });
	if (can(access, 'audit.read')) t.push({ href: routes.audit(), label: 'Audit log' });
	if (access.owner) t.push({ href: routes.diagnostics(), label: 'Diagnostics' });
	return t;
}
