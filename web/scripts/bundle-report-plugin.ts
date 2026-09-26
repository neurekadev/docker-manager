// Records the client chunk graph of `vite build` in
// .svelte-kit/bundle-report.json for scripts/verify-build.mjs (lazy-loading
// proof and bundle-size table, docs/adr/0002-frontend-libraries.md).
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, relative, resolve } from 'node:path';
import type { Plugin } from 'vite';

export interface ReportChunk {
	file: string;
	isEntry: boolean;
	isDynamicEntry: boolean;
	facade: string | null;
	imports: string[];
	dynamicImports: string[];
	css: string[];
	modules: string[];
	/** Rendered length per module, for proportional size attribution. */
	renderedLength: Record<string, number>;
}

export function bundleReport(): Plugin {
	let root = process.cwd();
	return {
		name: 'docker-manager-bundle-report',
		apply: 'build',
		configResolved(config) {
			root = config.root;
		},
		// writeBundle sees the final bundle (after Vite prunes CSS-only chunks).
		writeBundle(_options, bundle) {
			// SvelteKit runs a client and a server build; only the client ships.
			if (this.environment && this.environment.config.consumer !== 'client') return;
			const rel = (id: string) => relative(root, id).split('\\').join('/');
			const chunks: ReportChunk[] = [];
			for (const out of Object.values(bundle)) {
				if (out.type !== 'chunk') continue;
				const meta = (out as { viteMetadata?: { importedCss?: Set<string> } }).viteMetadata;
				chunks.push({
					file: out.fileName,
					isEntry: out.isEntry,
					isDynamicEntry: out.isDynamicEntry,
					facade: out.facadeModuleId ? rel(out.facadeModuleId) : null,
					imports: [...out.imports],
					dynamicImports: [...out.dynamicImports],
					css: [...(meta?.importedCss ?? [])],
					modules: out.moduleIds.map(rel),
					renderedLength: Object.fromEntries(
						Object.entries(out.modules).map(([id, m]) => [rel(id), m.renderedLength])
					)
				});
			}
			const file = resolve(root, '.svelte-kit/bundle-report.json');
			mkdirSync(dirname(file), { recursive: true });
			writeFileSync(file, JSON.stringify({ chunks }, null, '\t') + '\n');
		}
	};
}
