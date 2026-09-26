import { describe, expect, it } from 'vitest';
import { flushSync } from 'svelte';
import { ApiRequestError } from '$lib/api/client';
import { Connectivity } from './connectivity.svelte';
import { BACKGROUND_COLOR, THEME_COLOR, webManifest } from './manifest';
import appHtml from '../../app.html?raw';

describe('Connectivity', () => {
	it('follows browser online/offline events', () => {
		const c = new Connectivity();
		const target = new EventTarget();
		const stop = c.watch(target, false);
		flushSync();
		expect(c.state).toBe('offline');
		target.dispatchEvent(new Event('online'));
		flushSync();
		expect(c.state).toBe('online');
		target.dispatchEvent(new Event('offline'));
		flushSync();
		expect(c.state).toBe('offline');
		stop();
		target.dispatchEvent(new Event('online'));
		expect(c.browserOnline).toBe(false);
	});

	it('marks the manager unreachable only on network or bare gateway failures', () => {
		const c = new Connectivity();
		c.observe({ ok: false, error: new ApiRequestError('Failed to fetch', null) });
		expect(c.state).toBe('manager-unreachable');
		c.observe({ ok: true });
		expect(c.state).toBe('online');

		c.observe({ ok: false, error: new ApiRequestError('HTTP 502', 502) });
		expect(c.managerReachable).toBe(false);
		// A Docker Manager error body proves the manager answered, even with 503.
		c.observe({
			ok: false,
			error: new ApiRequestError('starting', 503, {
				code: 'unavailable',
				message: 'starting',
				details: [],
				requestId: 'r',
				retryable: true
			})
		});
		expect(c.managerReachable).toBe(true);
		c.observe({ ok: false, error: new ApiRequestError('nope', 404) });
		expect(c.managerReachable).toBe(true);
		// Programming errors say nothing about connectivity.
		c.observe({ ok: false, error: new Error('bug') });
		expect(c.managerReachable).toBe(true);
	});

	it('offline wins over manager state', () => {
		const c = new Connectivity();
		c.watch(new EventTarget(), false);
		c.observe({ ok: false, error: new ApiRequestError('x', null) });
		expect(c.state).toBe('offline');
	});
});

describe('web app manifest', () => {
	it('is installable and scoped to the whole origin', () => {
		expect(webManifest).toMatchObject({
			id: '/',
			name: 'Docker Manager',
			short_name: 'Docker Manager',
			start_url: '/',
			scope: '/',
			display: 'standalone'
		});
		const sizes = webManifest.icons.map(
			(i) => `${i.sizes}:${'purpose' in i ? i.purpose : 'any'}`
		);
		expect(sizes).toEqual(
			expect.arrayContaining(['192x192:any', '512x512:any', '512x512:maskable'])
		);
	});

	it('app.html links the manifest and repeats the token theme colour', () => {
		expect(appHtml).toContain('<link rel="manifest" href="/manifest.webmanifest" />');
		expect(appHtml).toContain(`<meta name="theme-color" content="${THEME_COLOR}" />`);
		expect(webManifest.theme_color).toBe(THEME_COLOR);
		expect(webManifest.background_color).toBe(BACKGROUND_COLOR);
	});
});
