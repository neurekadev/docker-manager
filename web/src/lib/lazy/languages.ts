// Languages of the file editor (#15), with no library code: the names
// shared by the editor, its language detection
// ($lib/features/files/language.ts) and the Markdown preview's fenced code.
// The parsers themselves load lazily in ./index.ts.

/**
 * YAML, JSON, Markdown, HTML, CSS, JavaScript and TypeScript have full
 * CodeMirror parsers; the others use a CodeMirror legacy stream mode (each
 * its own chunk); `text` is plain.
 */
export const EDITOR_LANGUAGES = [
	'yaml',
	'json',
	'shell',
	'dockerfile',
	'nginx',
	'properties',
	'toml',
	'xml',
	'html',
	'css',
	'scss',
	'less',
	'javascript',
	'typescript',
	'python',
	'go',
	'rust',
	'ruby',
	'lua',
	'perl',
	'powershell',
	'sql',
	'diff',
	'c',
	'cpp',
	'csharp',
	'java',
	'kotlin',
	'groovy',
	'swift',
	'protobuf',
	'cmake',
	'jinja2',
	'markdown',
	'text'
] as const;
export type EditorLanguage = (typeof EDITOR_LANGUAGES)[number];

/**
 * File extensions and the names a fenced code block uses (```bash), lower
 * case, to their language. A language's own name matches too.
 */
const ALIASES: Record<string, EditorLanguage> = {
	yml: 'yaml',
	jsonc: 'json',
	json5: 'json',
	geojson: 'json',
	webmanifest: 'json',
	ipynb: 'json',
	sh: 'shell',
	bash: 'shell',
	zsh: 'shell',
	ksh: 'shell',
	ash: 'shell',
	dash: 'shell',
	console: 'shell',
	shellsession: 'shell',
	docker: 'dockerfile',
	containerfile: 'dockerfile',
	env: 'properties',
	dotenv: 'properties',
	ini: 'properties',
	cfg: 'properties',
	cnf: 'properties',
	conf: 'properties',
	editorconfig: 'properties',
	gitconfig: 'properties',
	npmrc: 'properties',
	svg: 'xml',
	xsd: 'xml',
	xsl: 'xml',
	xslt: 'xml',
	plist: 'xml',
	rss: 'xml',
	atom: 'xml',
	csproj: 'xml',
	htm: 'html',
	xhtml: 'html',
	vue: 'html',
	svelte: 'html',
	sass: 'scss',
	js: 'javascript',
	mjs: 'javascript',
	cjs: 'javascript',
	jsx: 'javascript',
	node: 'javascript',
	ts: 'typescript',
	mts: 'typescript',
	cts: 'typescript',
	tsx: 'typescript',
	py: 'python',
	pyw: 'python',
	pyi: 'python',
	python3: 'python',
	golang: 'go',
	rs: 'rust',
	rb: 'ruby',
	gemspec: 'ruby',
	rake: 'ruby',
	pl: 'perl',
	pm: 'perl',
	ps1: 'powershell',
	psm1: 'powershell',
	psd1: 'powershell',
	pwsh: 'powershell',
	ps: 'powershell',
	psql: 'sql',
	mysql: 'sql',
	pgsql: 'sql',
	patch: 'diff',
	h: 'c',
	'c++': 'cpp',
	cc: 'cpp',
	cxx: 'cpp',
	hpp: 'cpp',
	hh: 'cpp',
	hxx: 'cpp',
	cs: 'csharp',
	'c#': 'csharp',
	kt: 'kotlin',
	kts: 'kotlin',
	gradle: 'groovy',
	jenkinsfile: 'groovy',
	proto: 'protobuf',
	j2: 'jinja2',
	jinja: 'jinja2',
	md: 'markdown',
	mdx: 'markdown',
	markdown: 'markdown',
	txt: 'text',
	log: 'text',
	plaintext: 'text',
	plain: 'text'
};

const KNOWN = new Set<string>(EDITOR_LANGUAGES);

/**
 * The language a file extension or a fenced code block's name stands for
 * ("yml", "bash", "py", "typescript"), or null when there is none.
 */
export function languageByName(name: string): EditorLanguage | null {
	const n = name.trim().toLowerCase();
	if (KNOWN.has(n)) return n as EditorLanguage;
	return ALIASES[n] ?? null;
}
