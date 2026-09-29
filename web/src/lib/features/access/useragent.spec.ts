import { describe, expect, it } from 'vitest';
import { describeUserAgent } from './useragent';

const UA = {
	firefoxWindows:
		'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0',
	firefoxLinux: 'Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0',
	chromeMac:
		'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36',
	edgeWindows:
		'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.0.0',
	operaWindows:
		'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 OPR/114.0.0.0',
	samsungAndroid:
		'Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0.0.0 Mobile Safari/537.36',
	chromeAndroid:
		'Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36',
	safariIphone:
		'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1',
	chromeIphone:
		'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/129.0.6668.69 Mobile/15E148 Safari/604.1',
	firefoxIpad:
		'Mozilla/5.0 (iPad; CPU OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/131.0 Mobile/15E148 Safari/605.1.15',
	safariMac:
		'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15',
	chromeOs:
		'Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36'
};

describe('describeUserAgent', () => {
	it.each([
		[UA.firefoxWindows, 'Firefox', 'Windows'],
		[UA.firefoxLinux, 'Firefox', 'Linux'],
		[UA.chromeMac, 'Chrome', 'macOS'],
		[UA.edgeWindows, 'Edge', 'Windows'],
		[UA.operaWindows, 'Opera', 'Windows'],
		[UA.samsungAndroid, 'Samsung Internet', 'Android'],
		[UA.chromeAndroid, 'Chrome', 'Android'],
		[UA.safariIphone, 'Safari', 'iOS'],
		[UA.chromeIphone, 'Chrome', 'iOS'],
		[UA.firefoxIpad, 'Firefox', 'iPadOS'],
		[UA.safariMac, 'Safari', 'macOS'],
		[UA.chromeOs, 'Chrome', 'ChromeOS']
	])('names the browser and system of %s', (ua, browser, os) => {
		expect(describeUserAgent(ua)).toEqual({ browser, os, label: `${browser} on ${os}` });
	});

	it('says unknown for what it does not recognise', () => {
		expect(describeUserAgent(undefined)).toEqual({
			browser: 'Unknown browser',
			os: 'unknown system',
			label: 'Unknown device'
		});
		expect(describeUserAgent('  ').label).toBe('Unknown device');
		expect(describeUserAgent('curl/8.9.1').label).toBe('Unknown device');
		expect(describeUserAgent('Mozilla/5.0 (Windows NT 10.0) SomeBot/1.0').label).toBe(
			'Unknown browser on Windows'
		);
		expect(describeUserAgent('Firefox/131.0').label).toBe('Firefox on unknown system');
	});

	it('does not take a Chromium browser for Safari', () => {
		// Chrome says "Safari/" but has no "Version/".
		expect(describeUserAgent(UA.chromeMac).browser).toBe('Chrome');
		expect(describeUserAgent('Mozilla/5.0 (Macintosh) Safari/605.1.15').browser).toBe(
			'Unknown browser'
		);
	});
});
