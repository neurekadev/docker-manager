// The icon of each notification service in the channel dialog's service
// picker (#142). Generic Lucide glyphs, never brand logos; the channel
// list shows the notification channel type's tile (RESOURCE_ICONS).
import BellDot from '@lucide/svelte/icons/bell-dot';
import BellRing from '@lucide/svelte/icons/bell-ring';
import Grid3x3 from '@lucide/svelte/icons/grid-3x3';
import Hash from '@lucide/svelte/icons/hash';
import Link from '@lucide/svelte/icons/link';
import Mail from '@lucide/svelte/icons/mail';
import MessageCircle from '@lucide/svelte/icons/message-circle';
import Send from '@lucide/svelte/icons/send';
import Smartphone from '@lucide/svelte/icons/smartphone';
import Users from '@lucide/svelte/icons/users';
import Webhook from '@lucide/svelte/icons/webhook';
import type { IconComponent } from '$lib/design/icons';
import type { ServiceId } from './services';

export const SERVICE_ICONS: Record<ServiceId, IconComponent> = {
	discord: MessageCircle,
	slack: Hash,
	teams: Users,
	telegram: Send,
	email: Mail,
	ntfy: BellRing,
	gotify: BellDot,
	pushover: Smartphone,
	matrix: Grid3x3,
	webhook: Webhook,
	other: Link
};
