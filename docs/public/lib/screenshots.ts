// The Screenshots page (app/screenshots): one carousel per screen size, each
// in the order of the app's side menu (a stack's tabs follow Stacks, the
// Settings pages come last). Images live in
// public/screenshots/<device>/<slug>.webp and are taken from a throwaway
// instance with the Docker Manager stack and a demo app
// (docs/internal/conventions/user-docs.md). Entries without an image for
// every size are left out when the site is built.

import { withBase } from './shared';

export const devices = [
  { id: 'desktop', label: 'Desktop', width: 1440, height: 900 },
  { id: 'tablet', label: 'Tablet', width: 820, height: 1180 },
  { id: 'mobile', label: 'Mobile', width: 390, height: 844 },
] as const;

export type DeviceId = (typeof devices)[number]['id'];

export interface Screenshot {
  slug: string;
  title: string;
  /** The docs page that explains it. */
  docs: string;
}

export const screenshots: Screenshot[] = [
  { slug: 'dashboard', title: 'Dashboard', docs: '/overview/' },
  { slug: 'environments', title: 'Environments', docs: '/environments/' },
  { slug: 'stacks', title: 'Stacks', docs: '/stacks/' },
  { slug: 'file-manager', title: 'File Manager', docs: '/file-manager/' },
  { slug: 'logs', title: 'Logs', docs: '/logs/' },
  { slug: 'terminal', title: 'Terminal', docs: '/terminal/' },
  { slug: 'containers', title: 'Containers', docs: '/containers/' },
  { slug: 'images', title: 'Images', docs: '/images/' },
  { slug: 'volumes', title: 'Volumes', docs: '/volumes/' },
  { slug: 'networks', title: 'Networks', docs: '/networks/' },
  { slug: 'builds', title: 'Builds', docs: '/builds/' },
  { slug: 'templates', title: 'Templates', docs: '/templates/' },
  { slug: 'registries', title: 'Registries', docs: '/registries/' },
  { slug: 'backups', title: 'Backups', docs: '/backups/' },
  { slug: 'updates', title: 'Updates', docs: '/updates/' },
  { slug: 'maintenance', title: 'Maintenance', docs: '/maintenance/' },
  { slug: 'jobs', title: 'Jobs', docs: '/overview/#how-it-works' },
  { slug: 'schedules', title: 'Schedules', docs: '/overview/#how-it-works' },
  { slug: 'access', title: 'Users & Groups', docs: '/users-and-groups/' },
  { slug: 'audit-log', title: 'Audit Log', docs: '/audit-log/' },
  { slug: 'migrations', title: 'Move to a new server', docs: '/migrations/' },
];

/** The image's path in public/. */
export function screenshotFile(slug: string, device: DeviceId): string {
  return `/screenshots/${device}/${slug}.webp`;
}

/** The image's URL on the site. */
export function screenshotSrc(slug: string, device: DeviceId): string {
  return withBase(screenshotFile(slug, device));
}
