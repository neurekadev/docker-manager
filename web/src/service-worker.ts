/// <reference types="@sveltejs/kit" />
/// <reference no-default-lib="true"/>
/// <reference lib="esnext" />
/// <reference lib="webworker" />
//
// Docker Manager service worker (#11, #23). SvelteKit compiles this file to
// /service-worker.js (registration is manual, see $lib/pwa/register.svelte.ts);
// @vite-pwa/sveltekit then injects the precache manifest into
// self.__WB_MANIFEST at build time. No Workbox code runs here: the routing
// and caching rules live in $lib/pwa/sw-core.ts and are unit-tested.
import { version } from '$service-worker';
import {
	cacheName,
	handleFetch,
	isSkipWaitingMessage,
	precache,
	precachePaths,
	removeStaleCaches,
	type PrecacheEntry,
	type SwDeps
} from '$lib/pwa/sw-core';

const sw = self as unknown as ServiceWorkerGlobalScope;
// workbox-build replaces the literal `self.__WB_MANIFEST` at build time.
const manifest = (self as unknown as { __WB_MANIFEST: PrecacheEntry[] }).__WB_MANIFEST;

const deps: SwDeps = {
	caches: sw.caches,
	fetch: (request) => sw.fetch(request),
	origin: sw.location.origin,
	cacheName: cacheName(version),
	precached: precachePaths(manifest)
};

sw.addEventListener('install', (event) => {
	// No skipWaiting() here: a new build waits until the user accepts the
	// "new version available" prompt, so open work is never interrupted.
	event.waitUntil(precache(deps));
});

sw.addEventListener('activate', (event) => {
	event.waitUntil(
		(async () => {
			await removeStaleCaches(deps);
			// Control already-open pages on first install (there is no older
			// worker then); after an accepted update the page reloads anyway.
			await sw.clients.claim();
		})()
	);
});

sw.addEventListener('fetch', (event) => {
	const response = handleFetch(event.request, deps);
	if (response) event.respondWith(response);
});

sw.addEventListener('message', (event) => {
	if (isSkipWaitingMessage(event.data)) void sw.skipWaiting();
});
