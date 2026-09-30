import { createMDX } from 'fumadocs-mdx/next';

const withMDX = createMDX();

// DOCS_BASE_PATH serves the site under a path of its domain, for example
// /docker-manager behind a reverse proxy (empty: the domain's root). Next
// prefixes pages and its own assets; lib/shared.ts `withBase` the paths
// written by hand.
const basePath = (process.env.DOCS_BASE_PATH ?? '').replace(/\/+$/, '');
if (basePath && !/^(\/[a-z0-9._-]+)+$/i.test(basePath)) {
  throw new Error(`DOCS_BASE_PATH must look like /docker-manager, got "${basePath}"`);
}

// DOCS_SITE_URL is the public origin (https://docs.neureka.dev) that link
// previews resolve the share image against. Empty: no absolute share URLs.
const siteUrl = (process.env.DOCS_SITE_URL ?? '').replace(/\/+$/, '');
if (siteUrl && !/^https:\/\/[a-z0-9.-]+(:\d+)?$/i.test(siteUrl)) {
  throw new Error(`DOCS_SITE_URL must be an https origin like https://docs.neureka.dev, got "${siteUrl}"`);
}

/** @type {import('next').NextConfig} */
const config = {
  // Static site served by nginx (Dockerfile): /page/ -> /page/index.html.
  output: 'export',
  trailingSlash: true,
  reactStrictMode: true,
  basePath,
  env: { NEXT_PUBLIC_BASE_PATH: basePath, NEXT_PUBLIC_SITE_URL: siteUrl },
};

export default withMDX(config);
