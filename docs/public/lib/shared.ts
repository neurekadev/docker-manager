export const appName = 'Docker Manager';
/** The docs' first page (the landing page is the site's root). */
export const docsRoute = '/overview/';

/** The path the site is served under (DOCS_BASE_PATH), '' at the domain's root. */
export const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? '';

/**
 * Prefixes a root-absolute path written by hand (images, files in public/,
 * the search index) with the base path. next/link adds it by itself.
 */
export function withBase(path: string): string {
  return basePath + path;
}
