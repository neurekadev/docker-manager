// Editor language detection (#15): Auto Detect is the default of every
// tab; the user can pick a language in the editor's language select. The
// order: an editor modeline (vim, Emacs), the file name, the extension,
// the shebang, then the content of a file whose name says nothing (JSON,
// XML/HTML, diff, Dockerfile, nginx, YAML, env/INI, Markdown). Only the
// start of the text is read, so it is cheap to run on every edit. Names
// match $lib/lazy EDITOR_LANGUAGES.
import { EDITOR_LANGUAGES, languageByName, type EditorLanguage } from '$lib/lazy/languages';
import type { SelectOption } from '$lib/ui';
import { basename, extension } from './paths';

/** A tab's language: Auto Detect, or the one the user picked. */
export type LanguageChoice = EditorLanguage | 'auto';

export const LANGUAGE_LABELS: Record<EditorLanguage, string> = {
	yaml: 'YAML',
	json: 'JSON',
	shell: 'Shell',
	dockerfile: 'Dockerfile',
	nginx: 'Nginx',
	properties: 'Env / INI / Properties',
	toml: 'TOML',
	xml: 'XML',
	html: 'HTML',
	css: 'CSS',
	scss: 'SCSS',
	less: 'Less',
	javascript: 'JavaScript',
	typescript: 'TypeScript',
	python: 'Python',
	go: 'Go',
	rust: 'Rust',
	ruby: 'Ruby',
	lua: 'Lua',
	perl: 'Perl',
	powershell: 'PowerShell',
	sql: 'SQL',
	diff: 'Diff',
	c: 'C',
	cpp: 'C++',
	csharp: 'C#',
	java: 'Java',
	kotlin: 'Kotlin',
	groovy: 'Groovy',
	swift: 'Swift',
	protobuf: 'Protocol Buffers',
	cmake: 'CMake',
	jinja2: 'Jinja',
	markdown: 'Markdown',
	text: 'Plain Text'
};

/**
 * The language select's options: Auto Detect first (naming what it
 * detected), then the languages by name, Plain Text last.
 */
export function languageOptions(detected: EditorLanguage): SelectOption[] {
	const named = EDITOR_LANGUAGES.filter((l) => l !== 'text')
		.map((l) => ({ value: l, label: LANGUAGE_LABELS[l] }))
		.sort((a, b) => a.label.localeCompare(b.label));
	return [
		{ value: 'auto', label: `Auto Detect (${LANGUAGE_LABELS[detected]})` },
		...named,
		{ value: 'text', label: LANGUAGE_LABELS.text }
	];
}

/** Whole file names (lower case) that say what a file is. */
const BY_NAME: Record<string, EditorLanguage> = {
	dockerfile: 'dockerfile',
	containerfile: 'dockerfile',
	'.bashrc': 'shell',
	'.bash_profile': 'shell',
	'.bash_login': 'shell',
	'.bash_logout': 'shell',
	'.bash_aliases': 'shell',
	'.profile': 'shell',
	'.zshrc': 'shell',
	'.zprofile': 'shell',
	'.zshenv': 'shell',
	'.zlogin': 'shell',
	'.kshrc': 'shell',
	pkgbuild: 'shell',
	apkbuild: 'shell',
	'nginx.conf': 'nginx',
	'cmakelists.txt': 'cmake',
	jenkinsfile: 'groovy',
	gemfile: 'ruby',
	rakefile: 'ruby',
	vagrantfile: 'ruby',
	podfile: 'ruby',
	brewfile: 'ruby',
	pipfile: 'toml',
	'cargo.lock': 'toml',
	'poetry.lock': 'toml',
	'.editorconfig': 'properties',
	'.gitconfig': 'properties',
	'.gitmodules': 'properties',
	'.npmrc': 'properties',
	'.pypirc': 'properties',
	'.babelrc': 'json',
	'.jshintrc': 'json'
};

/** Shebang interpreters (version suffix removed) and modeline names that are no language name. */
const INTERPRETERS: Record<string, EditorLanguage> = {
	sh: 'shell',
	bash: 'shell',
	zsh: 'shell',
	ksh: 'shell',
	mksh: 'shell',
	dash: 'shell',
	ash: 'shell',
	busybox: 'shell',
	node: 'javascript',
	nodejs: 'javascript',
	bun: 'javascript',
	deno: 'typescript',
	'ts-node': 'typescript',
	tsx: 'typescript',
	pwsh: 'powershell',
	pypy: 'python',
	luajit: 'lua',
	kscript: 'kotlin',
	conf: 'properties',
	dosini: 'properties'
};

/** The start of the text that detection reads. */
const HEAD = 4096;

/**
 * The language to highlight a file with, as Auto Detect chooses it from
 * its path and (when given) its text.
 */
export function detectLanguage(path: string, text = ''): EditorLanguage {
	const head = text.slice(text.charCodeAt(0) === 0xfeff ? 1 : 0, HEAD);
	const fromModeline =
		modeline(firstLines(head, 5)) ??
		modeline(text.slice(-1024).split('\n').slice(-5).join('\n'));
	if (fromModeline) return fromModeline;
	return byPath(path, head) ?? shebang(head) ?? sniff(head) ?? 'text';
}

function byPath(path: string, head: string): EditorLanguage | null {
	const lower = basename(path).toLowerCase();
	const named = BY_NAME[lower];
	if (named) return named;
	if (
		lower.startsWith('dockerfile.') ||
		lower.startsWith('containerfile.') ||
		lower.endsWith('.dockerfile')
	)
		return 'dockerfile';
	if (lower === '.env' || lower.startsWith('.env.')) return 'properties';
	const ext = extension(lower);
	if (ext === 'conf') return lower.includes('nginx') || isNginx(head) ? 'nginx' : 'properties';
	return ext ? languageByName(ext) : null;
}

/** "vim: set ft=sh :", "vi: filetype=yaml", "-*- mode: python -*-" (first or last lines). */
function modeline(text: string): EditorLanguage | null {
	const m =
		/\b(?:vim?|ex):.*?\b(?:ft|filetype|syntax)=([\w+#-]+)/.exec(text) ??
		/-\*-.*?\bmode:\s*([\w+#-]+).*?-\*-/i.exec(text) ??
		/-\*-\s*([\w+#-]+)\s*-\*-/.exec(text);
	if (!m) return null;
	const name = m[1].toLowerCase().replace(/-(script|mode)$/, '');
	return INTERPRETERS[name] ?? languageByName(name);
}

function firstLines(text: string, n: number): string {
	return text.split('\n', n).join('\n');
}

/** "#!/bin/bash", "#!/usr/bin/env -S python3 -u". */
function shebang(head: string): EditorLanguage | null {
	const m = /^#!\s*(\S+)([^\n]*)/.exec(head);
	if (!m) return null;
	let program = m[1].slice(m[1].lastIndexOf('/') + 1);
	if (program === 'env') {
		// The first argument that is no option and no VAR=value.
		program = m[2].split(/\s+/).find((a) => a && !a.startsWith('-') && !a.includes('=')) ?? '';
	}
	const name = program.toLowerCase().replace(/[\d.]+$/, '');
	return INTERPRETERS[name] ?? languageByName(name);
}

function isNginx(head: string): boolean {
	return /^\s*(server|http|events|stream|upstream\s+\S+|location\s+[^{\n]+)\s*\{/m.test(head);
}

/** The first lines that carry content: not empty, not a comment. */
function significant(head: string): string[] {
	return head
		.split('\n')
		.filter((l) => l.trim() !== '' && !/^\s*[#;]/.test(l))
		.slice(0, 30);
}

function share(lines: string[], re: RegExp): number {
	return lines.length ? lines.filter((l) => re.test(l)).length / lines.length : 0;
}

/** Guesses the language of a file whose name and shebang say nothing. */
function sniff(head: string): EditorLanguage | null {
	const start = head.trimStart();
	if (!start) return null;
	if (/^<\?xml\b/.test(start) || /^<svg\b/.test(start)) return 'xml';
	// Leading comments first; a comment's body never spans a "-->", so
	// many comments cannot make the match backtrack exponentially.
	if (/^(?:<!--(?:(?!-->)[\s\S])*-->\s*)*<(!doctype\s+html|html)\b/i.test(start)) return 'html';
	if (/^\{\s*("|\})/.test(start) || /^\[\s*([[{"\]\d-]|true|false|null)/.test(start))
		return 'json';
	if (/^(diff --git |diff -\w|--- \S[^\n]*\n\+\+\+ \S|Index: \S)/m.test(head)) return 'diff';
	const lines = significant(head);
	if (/^(FROM|ARG)\s+\S/.test(lines[0] ?? '') && /^FROM\s+\S/m.test(head)) return 'dockerfile';
	if (isNginx(head)) return 'nginx';
	if (/^(---|%YAML)(\s|$)/.test(start)) return 'yaml';
	if (lines.length >= 2) {
		if (share(lines, /^(export\s+)?[A-Za-z_][\w.]*=/) >= 0.8) return 'properties';
		if (
			share(lines, /^\s*(\[[^\]]+\]\s*$|[\w.-]+\s*=)/) >= 0.8 &&
			lines.some((l) => /^\s*\[[^\]]+\]\s*$/.test(l))
		)
			return 'properties';
		if (share(lines, /^\s*(- |-$|[\w"'./-]+\s*:(\s|$))/) >= 0.8) return 'yaml';
	}
	const first = head.split('\n').find((l) => l.trim() !== '') ?? '';
	if (/^#{1,6}\s+\S/.test(first) && /^(```|~~~|\s*[-*+] |\s*\d+\. )|\]\([^)]+\)/m.test(head))
		return 'markdown';
	return null;
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
