// Editor language detection (#15): by file name, then extension; the user
// can switch it in the editor's language select. Names match
// $lib/lazy EDITOR_LANGUAGES.
import type { EditorLanguage } from '$lib/lazy';
import { basename, extension } from './paths';

export const LANGUAGE_LABELS: Record<EditorLanguage, string> = {
	yaml: 'YAML',
	json: 'JSON',
	shell: 'Shell',
	dockerfile: 'Dockerfile',
	nginx: 'Nginx',
	properties: 'Env / properties',
	toml: 'TOML',
	xml: 'XML',
	markdown: 'Markdown',
	text: 'Plain text'
};

const BY_EXTENSION: Record<string, EditorLanguage> = {
	yaml: 'yaml',
	yml: 'yaml',
	json: 'json',
	jsonc: 'json',
	json5: 'json',
	sh: 'shell',
	bash: 'shell',
	zsh: 'shell',
	env: 'properties',
	properties: 'properties',
	ini: 'properties',
	cfg: 'properties',
	toml: 'toml',
	xml: 'xml',
	svg: 'xml',
	html: 'xml',
	md: 'markdown',
	markdown: 'markdown',
	txt: 'text',
	log: 'text'
};

/** The language to highlight `path` with. */
export function detectLanguage(path: string): EditorLanguage {
	const name = basename(path);
	const lower = name.toLowerCase();
	if (lower === 'dockerfile' || lower.startsWith('dockerfile.') || lower.endsWith('.dockerfile'))
		return 'dockerfile';
	if (lower === '.env' || lower.startsWith('.env.')) return 'properties';
	if (lower === 'nginx.conf' || (lower.endsWith('.conf') && lower.includes('nginx')))
		return 'nginx';
	if (lower === '.bashrc' || lower === '.profile') return 'shell';
	return BY_EXTENSION[extension(name)] ?? 'text';
}

/** Languages the Format button (and its Beautify entry) can reformat. */
export function formattable(l: EditorLanguage): l is 'yaml' | 'json' {
	return l === 'yaml' || l === 'json';
}

/**
 * Languages the Format menu's Minify entry can compact without changing
 * what the file means: JSON only (YAML, and so Compose files, depend on
 * their indentation and line breaks).
 */
export function minifiable(l: EditorLanguage): l is 'json' {
	return l === 'json';
}

/** Why Minify is off for a formattable language (shown under the entry). */
export function minifyUnavailable(l: EditorLanguage): string | null {
	if (minifiable(l)) return null;
	if (l === 'yaml') return "YAML depends on its indentation, so it can't be minified.";
	return `${LANGUAGE_LABELS[l]} can't be minified.`;
}

/** Image types previewed instead of edited (bounded, #15). */
export function imageType(path: string): string | null {
	switch (extension(basename(path))) {
		case 'png':
			return 'image/png';
		case 'jpg':
		case 'jpeg':
			return 'image/jpeg';
		case 'gif':
			return 'image/gif';
		case 'webp':
			return 'image/webp';
		default:
			return null;
	}
}

/** Archives the extraction job understands (zip, tar.gz). */
export function archiveFormat(name: string): 'zip' | 'tar.gz' | null {
	const l = name.toLowerCase();
	if (l.endsWith('.zip')) return 'zip';
	if (l.endsWith('.tar.gz') || l.endsWith('.tgz')) return 'tar.gz';
	return null;
}
