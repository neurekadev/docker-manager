import { createMDX } from 'fumadocs-mdx/next';

const withMDX = createMDX();

/** @type {import('next').NextConfig} */
const config = {
  // Static site served by nginx (Dockerfile): /page/ -> /page/index.html.
  output: 'export',
  trailingSlash: true,
  reactStrictMode: true,
};

export default withMDX(config);
