// Lucide icons addressable by name (service icons stored in display
// metadata, #22). Only these names are valid overrides; each is imported
// individually so unused icons never ship. Everywhere else, import icons
// directly: `import Globe from '@lucide/svelte/icons/globe'`.
import type { Component } from 'svelte';
import AppWindow from '@lucide/svelte/icons/app-window';
import Box from '@lucide/svelte/icons/box';
import Clapperboard from '@lucide/svelte/icons/clapperboard';
import Cog from '@lucide/svelte/icons/cog';
import Cpu from '@lucide/svelte/icons/cpu';
import Database from '@lucide/svelte/icons/database';
import Film from '@lucide/svelte/icons/film';
import Globe from '@lucide/svelte/icons/globe';
import HardDrive from '@lucide/svelte/icons/hard-drive';
import Layers from '@lucide/svelte/icons/layers';
import MessageSquare from '@lucide/svelte/icons/message-square';
import Server from '@lucide/svelte/icons/server';
import Shield from '@lucide/svelte/icons/shield';
import Workflow from '@lucide/svelte/icons/workflow';
import Zap from '@lucide/svelte/icons/zap';
import { SERVICE_ICON_CATEGORY } from './hue';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type IconComponent = Component<any>;

export const SERVICE_ICONS: Record<keyof typeof SERVICE_ICON_CATEGORY, IconComponent> = {
	globe: Globe,
	box: Box,
	server: Server,
	'app-window': AppWindow,
	database: Database,
	'hard-drive': HardDrive,
	layers: Layers,
	zap: Zap,
	cog: Cog,
	workflow: Workflow,
	clapperboard: Clapperboard,
	film: Film,
	shield: Shield,
	'message-square': MessageSquare,
	cpu: Cpu
};

/** The component of a service icon name ("box" when unknown). */
export function serviceIcon(name: string): IconComponent {
	return SERVICE_ICONS[name] ?? Box;
}
