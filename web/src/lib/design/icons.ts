// The type of a Lucide icon component. Import icons directly, each on its
// own so unused icons never ship: `import Globe from '@lucide/svelte/icons/globe'`.
// Resource types take theirs from RESOURCE_ICONS
// ($lib/features/common/resourceIcons).
import type { Component } from 'svelte';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type IconComponent = Component<any>;
