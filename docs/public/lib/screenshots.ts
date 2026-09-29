// The Screenshots page (app/screenshots): one entry per feature, in the
// docs' order. Images live in public/screenshots/<slug>-<device>.png and are
// taken from a fresh instance that holds only the Docker Manager stack
// (docs/internal/conventions/user-docs.md). Entries without images are
// left out when the site is built.

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
  { slug: 'dashboard', title: 'Dashboard', docs: '/docs/' },
  { slug: 'environments', title: 'Environments', docs: '/docs/environments/' },
  { slug: 'stacks', title: 'Stacks', docs: '/docs/stacks/' },
  { slug: 'containers', title: 'Containers', docs: '/docs/containers/' },
  { slug: 'images', title: 'Images', docs: '/docs/images/' },
  { slug: 'volumes', title: 'Volumes', docs: '/docs/volumes/' },
  { slug: 'networks', title: 'Networks', docs: '/docs/networks/' },
  { slug: 'builds', title: 'Builds', docs: '/docs/builds/' },
  { slug: 'templates', title: 'Templates', docs: '/docs/templates/' },
  { slug: 'registries', title: 'Registries', docs: '/docs/registries/' },
  { slug: 'backups', title: 'Backups', docs: '/docs/backups/' },
  { slug: 'updates', title: 'Updates', docs: '/docs/updates/' },
  { slug: 'maintenance', title: 'Maintenance', docs: '/docs/maintenance/' },
  { slug: 'file-manager', title: 'File Manager', docs: '/docs/file-manager/' },
  { slug: 'terminal', title: 'Terminal', docs: '/docs/terminal/' },
  { slug: 'logs', title: 'Logs', docs: '/docs/logs/' },
  { slug: 'migrations', title: 'Migrations', docs: '/docs/migrations/' },
];

export function screenshotSrc(slug: string, device: DeviceId): string {
  return `/screenshots/${slug}-${device}.png`;
}
