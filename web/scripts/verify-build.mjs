#!/usr/bin/env node
// Post-build checks for web/build/app (run after `npm run build` by
// scripts/check.sh and the build job of .github/workflows/CI.yaml):
//
//   1. Lazy loading: CodeMirror, ECharts and xterm.js modules live only in
//      chunks that no entry chunk imports statically (dynamic import only).
//   2. PWA: the service worker's injected precache list contains only
//      versioned static files and the SPA shell (never /api or /agent), and
//      covers every content-hashed build file; the manifest is installable
//      and its icons exist with the declared pixel sizes.
//   3. Bundle sizes: prints the initial-load and per-library chunk sizes
//      (raw and gzip) recorded in docs/adr/0002-frontend-libraries.md.
//
//   node scripts/verify-build.mjs [--markdown]
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { gzipSync } from 'node:zlib';

const web = resolve(import.meta.dirname, '..');
const out = join(web, 'build/app');
const problems = [];
const fail = (msg) => problems.push(msg);

// ------------------------------------------------------------ chunk graph
const report = JSON.parse(readFileSync(join(web, '.svelte-kit/bundle-report.json'), 'utf8'));
const byFile = new Map(report.chunks.map((c) => [c.file, c]));

const LAZY = {
	codemirror: /node_modules\/(codemirror|@codemirror|@lezer|crelt|style-mod|w3c-keyname)\//,
	echarts: /node_modules\/(echarts|zrender|tslib)\//,
	xterm: /node_modules\/@xterm\//,
	// The editor's YAML formatter (#15): loaded by formatDocument() only.
	yaml: /node_modules\/yaml\//
};
const TRACKED = {
	...LAZY,
	'bits-ui':
		/node_modules\/(bits-ui|svelte-toolbelt|runed|@floating-ui|@internationalized|tabbable|style-to-object|inline-style-parser)\//,
	'svelte-query': /node_modules\/@tanstack\//,
	lucide: /node_modules\/@lucide\//,
	'openapi-fetch': /node_modules\/openapi-fetch\//,
	'svelte + kit': /node_modules\/(svelte|@sveltejs|devalue|esm-env|clsx)\//
};

function staticClosure(files) {
	const seen = new Set();
	const stack = [...files];
	while (stack.length) {
		const f = stack.pop();
		if (seen.has(f) || !byFile.has(f)) continue;
		seen.add(f);
		stack.push(...byFile.get(f).imports);
	}
	return seen;
}

const entries = report.chunks.filter((c) => c.isEntry).map((c) => c.file);
if (entries.length === 0) fail('bundle report has no entry chunks');
const staticallyReachable = staticClosure(entries);
for (const [lib, re] of Object.entries(LAZY)) {
	const chunks = report.chunks.filter((c) => c.modules.some((m) => re.test(m)));
	if (chunks.length === 0) fail(`${lib}: no chunk contains it (lazy entry point missing?)`);
	for (const c of chunks) {
		if (staticallyReachable.has(c.file)) {
			fail(
				`${lib}: ${c.file} is statically imported by an entry chunk; load it with import()`
			);
		}
	}
}

// Initial load of "/": kit entries + root layout + the (app) group layout
// (the signed-in shell, #22) + the dashboard page node.
const isNode = (c, n) => c.facade?.endsWith(`/nodes/${n}.js`);
const nodeWith = (suffix) =>
	report.chunks.find(
		(c) =>
			c.isEntry && c.facade?.includes('/nodes/') && c.modules.some((m) => m.endsWith(suffix))
	);
const rootPage = nodeWith('src/routes/(app)/+page.svelte') ?? nodeWith('src/routes/+page.svelte');
const appLayout = nodeWith('src/routes/(app)/+layout.svelte');
const initialRoots = report.chunks
	.filter(
		(c) =>
			c.isEntry &&
			(c.facade?.endsWith('client-optimized/app.js') ||
				c.facade?.includes('runtime/client/entry.js') ||
				isNode(c, 0))
	)
	.map((c) => c.file);
if (rootPage) initialRoots.push(rootPage.file);
else fail('could not find the chunk of the / page (src/routes/(app)/+page.svelte)');
if (appLayout) initialRoots.push(appLayout.file);
if (initialRoots.length < 4)
	fail(`expected start, app, layout and page entries, got ${initialRoots.join(', ')}`);
const initial = staticClosure(initialRoots);

function size(file) {
	const b = readFileSync(join(out, '_app/immutable', file.replace(/^_app\/immutable\//, '')));
	return { raw: b.length, gzip: gzipSync(b, { level: 9 }).length };
}
function sum(files) {
	let raw = 0;
	let gzip = 0;
	for (const f of files) {
		const s = size(f);
		raw += s.raw;
		gzip += s.gzip;
	}
	return { raw, gzip };
}
const initialCss = new Set([...initial].flatMap((f) => byFile.get(f).css));
const rows = [];
rows.push(['initial load of / (JS)', initial.size, sum(initial)]);
rows.push(['initial load of / (CSS)', initialCss.size, sum(initialCss)]);
// Per library: each chunk's real size is split across its modules in
// proportion to their rendered length (chunks mix code from many packages).
for (const [lib, re] of Object.entries(TRACKED)) {
	let raw = 0;
	let gzip = 0;
	let inInitial = false;
	const files = new Set();
	for (const c of report.chunks) {
		const lens = Object.entries(c.renderedLength);
		const total = lens.reduce((a, [, n]) => a + n, 0);
		const mine = lens.filter(([id]) => re.test(id)).reduce((a, [, n]) => a + n, 0);
		if (!mine || !total) continue;
		files.add(c.file);
		if (initial.has(c.file)) inInitial = true;
		const s = size(c.file);
		raw += (s.raw * mine) / total;
		gzip += (s.gzip * mine) / total;
	}
	const how = inInitial ? 'in initial load' : lib in LAZY ? 'lazy' : 'route-split';
	rows.push([`${lib} (${how})`, files.size, { raw, gzip }]);
}
const allJs = report.chunks.map((c) => c.file);
rows.push(['all JS chunks', allJs.length, sum(allJs)]);

// ------------------------------------------------------------ PWA
const swSource = readFileSync(join(out, 'service-worker.js'), 'utf8');
if (swSource.includes('__WB_MANIFEST'))
	fail('service-worker.js: precache manifest was not injected');
const m = swSource.match(/\[\{"(?:revision|url)":[\s\S]*?\}\]/);
let precacheEntries = [];
if (!m) fail('service-worker.js: cannot find the injected precache list');
else precacheEntries = JSON.parse(m[0]);
const allowed = [
	/^_app\/immutable\/[\w./-]+\.(js|css|woff2)$/,
	/^index\.html$/,
	/^manifest\.webmanifest$/,
	/^icons\/[\w.-]+\.(png|svg)$/,
	/^favicon\.ico$/
];
for (const e of precacheEntries) {
	const url = e.url.replace(/^\//, '');
	if (/^(api|agent)(\/|$)/.test(url)) fail(`precache contains API/agent path ${e.url}`);
	if (!allowed.some((re) => re.test(url))) fail(`precache contains unexpected ${e.url}`);
	if (!url.startsWith('_app/immutable/') && !e.revision)
		fail(`unhashed precache entry without revision: ${e.url}`);
}
const precached = new Set(precacheEntries.map((e) => e.url.replace(/^\//, '')));
for (const must of ['index.html', 'manifest.webmanifest']) {
	if (!precached.has(must)) fail(`precache lacks ${must}`);
}
function walk(dir, prefix = '') {
	return readdirSync(dir).flatMap((n) =>
		statSync(join(dir, n)).isDirectory()
			? walk(join(dir, n), `${prefix}${n}/`)
			: [`${prefix}${n}`]
	);
}
for (const f of walk(join(out, '_app/immutable'), '_app/immutable/')) {
	if (/\.(js|css|woff2)$/.test(f) && !precached.has(f))
		fail(`versioned build file not precached: ${f}`);
}
let precacheBytes = 0;
for (const u of precached) precacheBytes += statSync(join(out, u)).size;

const manifest = JSON.parse(readFileSync(join(out, 'manifest.webmanifest'), 'utf8'));
for (const [k, v] of Object.entries({
	name: 'DockYard',
	start_url: '/',
	scope: '/',
	display: 'standalone',
	id: '/'
})) {
	if (manifest[k] !== v)
		fail(`manifest ${k} = ${JSON.stringify(manifest[k])}, want ${JSON.stringify(v)}`);
}
function pngSize(file) {
	if (!existsSync(file)) return 'missing';
	const b = readFileSync(file);
	if (b.toString('ascii', 1, 4) !== 'PNG') return 'not a PNG';
	return `${b.readUInt32BE(16)}x${b.readUInt32BE(20)}`;
}
for (const icon of manifest.icons ?? []) {
	const actual = pngSize(join(out, icon.src.replace(/^\//, '')));
	if (actual !== icon.sizes) fail(`manifest icon ${icon.src}: ${actual}, declared ${icon.sizes}`);
	if (!precached.has(icon.src.replace(/^\//, '')))
		fail(`manifest icon not precached: ${icon.src}`);
}
for (const size of ['192x192', '512x512']) {
	if (!(manifest.icons ?? []).some((i) => i.sizes === size))
		fail(`manifest lacks a ${size} icon`);
}
if (!(manifest.icons ?? []).some((i) => i.purpose === 'maskable'))
	fail('manifest lacks a maskable icon');
const index = readFileSync(join(out, 'index.html'), 'utf8');
if (!index.includes('<link rel="manifest" href="/manifest.webmanifest"'))
	fail('index.html does not link the manifest');

// ------------------------------------------------------------ output
const kb = (n) => (n / 1024).toFixed(1);
const markdown = process.argv.includes('--markdown');
if (markdown) {
	console.log('| bundle | chunks | raw KiB | gzip KiB |');
	console.log('| --- | ---: | ---: | ---: |');
	for (const [name, n, s] of rows)
		console.log(`| ${name} | ${n} | ${kb(s.raw)} | ${kb(s.gzip)} |`);
	console.log(`| service-worker precache | ${precached.size} files | ${kb(precacheBytes)} | |`);
} else {
	console.log('bundle sizes (KiB, raw / gzip -9):');
	for (const [name, n, s] of rows) {
		console.log(
			`  ${name.padEnd(36)} ${String(n).padStart(3)} chunks  ${kb(s.raw).padStart(8)} / ${kb(s.gzip).padStart(7)}`
		);
	}
	console.log(`  service-worker precache: ${precached.size} files, ${kb(precacheBytes)} KiB raw`);
}

if (problems.length) {
	console.error('\nverify-build: FAILED');
	for (const p of problems) console.error(`  - ${p}`);
	process.exit(1);
}
console.log('verify-build: ok (lazy chunks, precache list, manifest, icons)');
