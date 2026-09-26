// Docker Manager's UI is a client-rendered SPA served by the Go manager: no SSR,
// no prerendered routes. Deep links fall back to index.html (adapter-static
// `fallback`), which the manager serves for every non-asset path.
export const ssr = false;
export const prerender = false;
