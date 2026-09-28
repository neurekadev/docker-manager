// One icon and tile colour per resource type (#22): the tile on the
// object's page header, its empty state, the small tile before its name in
// every list (IconCell, NameCell `icon`), the sidebar entry of its section
// and its ⌘K search hits all read this map, so a type looks the same
// everywhere. Stacks and services have no icon of their own: every service
// shows the service tile, every stack the stack tile (StackIcon shows the
// image of the template a stack was created from instead, when it has one).
import Activity from '@lucide/svelte/icons/activity';
import Archive from '@lucide/svelte/icons/archive';
import Box from '@lucide/svelte/icons/box';
import CalendarClock from '@lucide/svelte/icons/calendar-clock';
import Camera from '@lucide/svelte/icons/camera';
import Container from '@lucide/svelte/icons/container';
import FileCode from '@lucide/svelte/icons/file-code';
import Fingerprint from '@lucide/svelte/icons/fingerprint';
import GitBranch from '@lucide/svelte/icons/git-branch';
import Hammer from '@lucide/svelte/icons/hammer';
import HardDrive from '@lucide/svelte/icons/hard-drive';
import KeyRound from '@lucide/svelte/icons/key-round';
import Layers from '@lucide/svelte/icons/layers';
import LayoutTemplate from '@lucide/svelte/icons/layout-template';
import MailPlus from '@lucide/svelte/icons/mail-plus';
import Network from '@lucide/svelte/icons/network';
import PackageCheck from '@lucide/svelte/icons/package-check';
import Server from '@lucide/svelte/icons/server';
import User from '@lucide/svelte/icons/user';
import UsersRound from '@lucide/svelte/icons/users-round';
import Workflow from '@lucide/svelte/icons/workflow';
import Wrench from '@lucide/svelte/icons/wrench';
import { SERVICE_COLOR, type TileColor } from '$lib/design/hue';
import type { IconComponent } from '$lib/design/icons';

export interface ResourceIcon {
	icon: IconComponent;
	color: TileColor;
}

export const RESOURCE_ICONS = {
	environment: { icon: Server, color: 'blue' },
	stack: { icon: Layers, color: 'blue' },
	service: { icon: Workflow, color: SERVICE_COLOR },
	container: { icon: Container, color: 'blue' },
	image: { icon: Box, color: 'blue' },
	volume: { icon: HardDrive, color: 'teal' },
	network: { icon: Network, color: 'indigo' },
	build: { icon: Hammer, color: 'violet' },
	buildDefinition: { icon: FileCode, color: 'violet' },
	template: { icon: LayoutTemplate, color: 'violet' },
	templateRegistry: { icon: Archive, color: 'violet' },
	registry: { icon: KeyRound, color: 'slate' },
	gitCredential: { icon: GitBranch, color: 'slate' },
	backupPolicy: { icon: CalendarClock, color: 'teal' },
	backupRepository: { icon: HardDrive, color: 'teal' },
	backup: { icon: Archive, color: 'teal' },
	snapshot: { icon: Camera, color: 'slate' },
	updatePolicy: { icon: PackageCheck, color: 'violet' },
	maintenancePolicy: { icon: Wrench, color: 'slate' },
	job: { icon: Activity, color: 'violet' },
	schedule: { icon: CalendarClock, color: 'violet' },
	user: { icon: User, color: 'blue' },
	group: { icon: UsersRound, color: 'indigo' },
	invitation: { icon: MailPlus, color: 'blue' },
	apiToken: { icon: KeyRound, color: 'violet' },
	passkey: { icon: Fingerprint, color: 'slate' }
} as const satisfies Record<string, ResourceIcon>;

export type ResourceKind = keyof typeof RESOURCE_ICONS;

/** The icon and tile colour of a resource type. */
export function resourceIcon(kind: ResourceKind): ResourceIcon {
	return RESOURCE_ICONS[kind];
}

/**
 * An environment's tile: the environment colour while it is online, slate
 * while it is offline (as on its page and its dashboard card).
 */
export function environmentIcon(online: boolean): ResourceIcon {
	return { icon: RESOURCE_ICONS.environment.icon, color: online ? 'blue' : 'slate' };
}

/** The resource a schedule belongs to (the policy it runs), by schedule kind. */
export function scheduleResource(kind: string): ResourceKind {
	switch (kind) {
		case 'backup':
			return 'backupPolicy';
		case 'backup_verification':
			return 'backupRepository';
		case 'update_check':
		case 'update_run':
			return 'updatePolicy';
		case 'prune':
			return 'maintenancePolicy';
		default:
			return 'schedule';
	}
}
