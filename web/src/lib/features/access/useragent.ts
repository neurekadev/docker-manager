// Signed-in devices (#16): a browser's User-Agent in words ("Firefox on
// Windows"). Deliberately small and dependency-free: it names the common
// browsers and systems and falls back to "Unknown" for the rest. Pure
// (useragent.spec.ts).

export interface DeviceDescription {
	browser: string;
	os: string;
	/** "Firefox on Windows"; "Unknown device" when nothing is recognised. */
	label: string;
}

export const UNKNOWN_BROWSER = 'Unknown browser';
export const UNKNOWN_OS = 'unknown system';
export const UNKNOWN_DEVICE = 'Unknown device';

// Order matters: Edge, Opera and Samsung Internet also say "Chrome/", and
// every Chromium browser also says "Safari/".
const BROWSERS: [RegExp, string][] = [
	[/\bEdg(?:e|A|iOS)?\//, 'Edge'],
	[/\bOPR\//, 'Opera'],
	[/\bSamsungBrowser\//, 'Samsung Internet'],
	[/\b(?:Chrome|CriOS)\//, 'Chrome'],
	[/\b(?:Firefox|FxiOS)\//, 'Firefox']
];

// Order matters: iPhones say "like Mac OS X", Android and ChromeOS say
// "Linux" or "X11".
const SYSTEMS: [RegExp, string][] = [
	[/Windows/, 'Windows'],
	[/iPad/, 'iPadOS'],
	[/iPhone|iPod/, 'iOS'],
	[/Android/, 'Android'],
	[/\bCrOS\b/, 'ChromeOS'],
	[/Mac OS X|Macintosh/, 'macOS'],
	[/Linux/, 'Linux']
];

function browserOf(ua: string): string | undefined {
	for (const [re, name] of BROWSERS) if (re.test(ua)) return name;
	if (/\bSafari\//.test(ua) && /\bVersion\//.test(ua)) return 'Safari';
	return undefined;
}

function systemOf(ua: string): string | undefined {
	for (const [re, name] of SYSTEMS) if (re.test(ua)) return name;
	return undefined;
}

/** The browser and operating system of a User-Agent, in words. */
export function describeUserAgent(ua: string | undefined): DeviceDescription {
	const s = (ua ?? '').trim();
	const browser = s ? browserOf(s) : undefined;
	const os = s ? systemOf(s) : undefined;
	if (!browser && !os) return { browser: UNKNOWN_BROWSER, os: UNKNOWN_OS, label: UNKNOWN_DEVICE };
	const b = browser ?? UNKNOWN_BROWSER;
	const o = os ?? UNKNOWN_OS;
	return { browser: b, os: o, label: `${b} on ${o}` };
}
