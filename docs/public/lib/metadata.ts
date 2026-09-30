import type { Metadata } from 'next';
import { appName, siteUrl, tagline, withBase } from './shared';

// Link previews (Discord, Slack, X, ...) for every page: the logo as the
// share image, as an absolute URL (metadataBase + base path; Next does not
// add the base path to metadata URLs). A page's own openGraph replaces its
// parent's, so every page sets all of it through this helper.
const image = { url: withBase('/logo-512.png'), width: 512, height: 512, alt: `${appName} logo` };

export function shareMetadata(title: string = appName, description: string = tagline): Metadata {
  return {
    description,
    openGraph: { type: 'website', siteName: appName, title, description, images: [image] },
    twitter: { card: 'summary', title, description, images: [image] },
  };
}

export const metadataBase = siteUrl ? new URL(siteUrl) : undefined;
