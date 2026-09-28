// Sidebar navigation (#22) and its permission filter (#17). Items the user
// cannot use are hidden, not disabled. The filter only decides what to
// show; the server still authorizes every request.
import Activity from '@lucide/svelte/icons/activity';
import Box from '@lucide/svelte/icons/box';
import CalendarClock from '@lucide/svelte/icons/calendar-clock';
import Container from '@lucide/svelte/icons/container';
import DatabaseBackup from '@lucide/svelte/icons/database-backup';
import Hammer from '@lucide/svelte/icons/hammer';
import HardDrive from '@lucide/svelte/icons/hard-drive';
import KeyRound from '@lucide/svelte/icons/key-round';
import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard';
import LayoutTemplate from '@lucide/svelte/icons/layout-template';
import Layers from '@lucide/svelte/icons/layers';
import Network from '@lucide/svelte/icons/network';
import PackageCheck from '@lucide/svelte/icons/package-check';
import Server from '@lucide/svelte/icons/server';
import Settings from '@lucide/svelte/icons/settings';
import Users from '@lucide/svelte/icons/users';
import Wrench from '@lucide/svelte/icons/wrench';
import type { IconComponent } from '$lib/design/icons';
import type { MyPermissions } from '$lib/api/client';
import { routes } from '$lib/routes';

/** What the navigation needs to know about the caller's permissions. */
export interface Access {
	owner: boolean;
	/** Capability keys allowed on at least one scope. */
	allowed: ReadonlySet<string>;
	/** Environments visible to the caller (minimal or full). */
	environments: number;
}

export function accessOf(p: MyPermissions | undefined | null): Access {
	if (!p) return { owner: false, allowed: new Set(), environments: 0 };
	const allowed = new Set(p.entries.filter((e) => e.allowed).map((e) => e.capability));
	return { owner: p.owner, allowed, environments: p.environments.length };
}

/** True when any allowed capability starts with one of the prefixes. */
export function hasAny(a: Access, ...prefixes: string[]): boolean {
	if (a.owner) return true;
	for (const c of a.allowed) if (prefixes.some((p) => c === p || c.startsWith(p))) return true;
	return false;
}

/** The caller holds no grant at all: the Restricted empty state (#17). */
export function isRestricted(a: Access): boolean {
	return !a.owner && a.allowed.size === 0 && a.environments === 0;
}

export type NavGroup = 'overview' | 'resources' | 'operations' | 'admin';

export interface NavItem {
	id: string;
	label: string;
	href: string;
	icon: IconComponent;
	/** The sidebar group (NAV_GROUPS: a small label in the full sidebar, a divider in the rail). */
	group: NavGroup;
	visible: (a: Access) => boolean;
}

/** Sidebar groups in order; the first one has no label. */
export const NAV_GROUPS: { id: NavGroup; label?: string }[] = [
	{ id: 'overview' },
	{ id: 'resources', label: 'Docker' },
	{ id: 'operations', label: 'Automation' },
	{ id: 'admin', label: 'Administration' }
];

export const NAV_ITEMS: NavItem[] = [
	{
		id: 'dashboard',
		label: 'Dashboard',
		href: routes.dashboard(),
		icon: LayoutDashboard,
		group: 'overview',
		visible: () => true
	},
	{
		id: 'environments',
		label: 'Environments',
		href: routes.environments(),
		icon: Server,
		group: 'overview',
		visible: (a) => a.environments > 0 || hasAny(a, 'environment.', 'agent.')
	},
	{
		id: 'stacks',
		label: 'Stacks',
		href: routes.stacks(),
		icon: Layers,
		group: 'resources',
		visible: (a) => hasAny(a, 'stack.')
	},
	{
		id: 'containers',
		label: 'Containers',
		href: routes.containers(),
		icon: Container,
		group: 'resources',
		visible: (a) => hasAny(a, 'container.')
	},
	{
		id: 'images',
		label: 'Images',
		href: routes.images(),
		icon: Box,
		group: 'resources',
		visible: (a) => hasAny(a, 'image.')
	},
	{
		id: 'volumes',
		label: 'Volumes',
		href: routes.volumes(),
		icon: HardDrive,
		group: 'resources',
		visible: (a) => hasAny(a, 'volume.')
	},
	{
		id: 'networks',
		label: 'Networks',
		href: routes.networks(),
		icon: Network,
		group: 'resources',
		visible: (a) => hasAny(a, 'network.')
	},
	{
		id: 'builds',
		label: 'Builds',
		href: routes.builds(),
		icon: Hammer,
		group: 'resources',
		visible: (a) => hasAny(a, 'image.build', 'build_definition.', 'git_credential.')
	},
	{
		id: 'templates',
		label: 'Templates',
		href: routes.templates(),
		icon: LayoutTemplate,
		group: 'resources',
		visible: (a) => hasAny(a, 'template.')
	},
	{
		id: 'registries',
		label: 'Registries',
		href: routes.registries(),
		icon: KeyRound,
		group: 'resources',
		visible: (a) => hasAny(a, 'registry.')
	},
	{
		id: 'backups',
		label: 'Backups',
		href: routes.backups(),
		icon: DatabaseBackup,
		group: 'operations',
		visible: (a) => hasAny(a, 'backup.', 'backup_policy.', 'backup_repository.')
	},
	{
		id: 'updates',
		label: 'Updates',
		href: routes.updates(),
		icon: PackageCheck,
		group: 'operations',
		visible: (a) => hasAny(a, 'update.', 'update_policy.')
	},
	{
		id: 'maintenance',
		label: 'Maintenance',
		href: routes.maintenance(),
		icon: Wrench,
		group: 'operations',
		visible: (a) => hasAny(a, 'maintenance', 'maintenance_policy.')
	},
	{
		id: 'jobs',
		label: 'Jobs',
		href: routes.jobs(),
		icon: Activity,
		group: 'operations',
		// Jobs are visible through their targets' capabilities: anyone with a grant.
		visible: (a) => !isRestricted(a)
	},
	{
		id: 'schedules',
		label: 'Schedules',
		href: routes.schedules(),
		icon: CalendarClock,
		group: 'operations',
		// GET /schedules shows the policies the caller may read (#13 kinds).
		visible: (a) =>
			hasAny(
				a,
				'backup_policy.read',
				'update_policy.read',
				'maintenance_policy.read',
				'backup_repository.read'
			)
	},
	{
		id: 'access',
		label: 'Access',
		href: routes.access(),
		icon: Users,
		group: 'admin',
		visible: (a) => a.owner
	},
	{
		id: 'settings',
		label: 'Settings',
		href: routes.settings(),
		icon: Settings,
		group: 'admin',
		visible: () => true
	}
];

export function visibleNav(a: Access, items: NavItem[] = NAV_ITEMS): NavItem[] {
	return items.filter((i) => i.visible(a));
}

/** The nav item a path belongs to (longest matching prefix). */
export function activeNav(path: string, items: NavItem[] = NAV_ITEMS): NavItem | undefined {
	let best: NavItem | undefined;
	for (const i of items) {
		const match =
			i.href === '/' ? path === '/' : path === i.href || path.startsWith(i.href + '/');
		if (match && (!best || i.href.length > best.href.length)) best = i;
	}
	// Environment-scoped Docker objects belong to their list's section.
	if (!best || best.id === 'environments') {
		const m = path.match(/^\/environments\/[^/]+\/(containers|images|volumes|networks)(\/|$)/);
		if (m) return items.find((i) => i.id === m[1]) ?? best;
	}
	return best;
}
