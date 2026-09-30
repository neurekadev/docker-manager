export const appName = 'Docker Manager';
/** The landing page's one line, also the site's share description. */
export const tagline = 'One pane of glass for your infrastructure.';
/** The logo's blue: the accent in app/global.css and the strip of link previews. */
export const brandColor = '#52a3f7';
/** The docs' first page (the landing page is the site's root). */
export const docsRoute = '/overview/';

/** The path the site is served under (DOCS_BASE_PATH), '' at the domain's root. */
export const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? '';

/**
 * The public origin (DOCS_SITE_URL, e.g. https://docs.neureka.dev): link
 * previews need absolute URLs for the share image.
 */
export const siteUrl = process.env.NEXT_PUBLIC_SITE_URL || undefined;

/**
 * Prefixes a root-absolute path written by hand (images, files in public/,
 * the search index) with the base path. next/link adds it by itself.
 */
export function withBase(path: string): string {
  return basePath + path;
}
