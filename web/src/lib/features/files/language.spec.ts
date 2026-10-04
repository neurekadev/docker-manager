// Auto Detect (#15): modeline, file name, extension, shebang, then the
// content of files whose name says nothing; the language select lists
// Auto Detect first.
import { describe, expect, it } from 'vitest';
import { languageByName } from '$lib/lazy/languages';
import { tabLanguage } from './editor.svelte';
import { detectLanguage, languageOptions } from './language';

describe('detectLanguage', () => {
	it('reads file names and extensions', () => {
		expect(detectLanguage('compose.yml')).toBe('yaml');
		expect(detectLanguage('Containerfile')).toBe('dockerfile');
		expect(detectLanguage('build/app.Dockerfile')).toBe('dockerfile');
		expect(detectLanguage('.bashrc')).toBe('shell');
		expect(detectLanguage('Jenkinsfile')).toBe('groovy');
		expect(detectLanguage('CMakeLists.txt')).toBe('cmake');
		expect(detectLanguage('Gemfile')).toBe('ruby');
		expect(detectLanguage('main.go')).toBe('go');
		expect(detectLanguage('app.tsx')).toBe('typescript');
		expect(detectLanguage('script.ps1')).toBe('powershell');
		expect(detectLanguage('index.html')).toBe('html');
		expect(detectLanguage('styles.scss')).toBe('scss');
		expect(detectLanguage('schema.sql')).toBe('sql');
		expect(detectLanguage('fix.patch')).toBe('diff');
		expect(detectLanguage('config.ini')).toBe('properties');
		expect(detectLanguage('notes.txt', '#!/bin/sh\n')).toBe('text');
	});

	it('reads shebangs of files without an extension', () => {
		expect(detectLanguage('entrypoint', '#!/bin/bash\nset -e\n')).toBe('shell');
		expect(detectLanguage('run', '#!/bin/sh\n')).toBe('shell');
		expect(detectLanguage('tool', '#!/usr/bin/env python3\nprint(1)\n')).toBe('python');
		expect(detectLanguage('tool', '#!/usr/bin/env -S node --no-warnings\n')).toBe('javascript');
		expect(detectLanguage('tool', '#!/usr/bin/env DEBUG=1 ruby\n')).toBe('ruby');
		expect(detectLanguage('tool', '#!/usr/bin/pwsh\n')).toBe('powershell');
		expect(detectLanguage('tool', '#!/usr/bin/env unknown\n')).toBe('text');
	});

	it('follows an editor modeline before anything else', () => {
		expect(detectLanguage('hooks/post', '# vim: set ft=sh :\necho hi\n')).toBe('shell');
		expect(detectLanguage('notes.txt', 'a: 1\n# vim: ft=yaml\n')).toBe('yaml');
		expect(detectLanguage('build', '# -*- mode: python -*-\n')).toBe('python');
		// Only the first and last lines count.
		const middle = `a\n\n\n\n\n\n vim: ft=yaml\n\n\n\n\n\nb\n`;
		expect(detectLanguage('LICENSE', middle)).toBe('text');
	});

	it('sniffs the content of files whose name says nothing', () => {
		expect(detectLanguage('data', '{\n  "a": 1\n}\n')).toBe('json');
		expect(detectLanguage('data', '[{"a": 1}]')).toBe('json');
		expect(detectLanguage('feed', '<?xml version="1.0"?>\n<rss/>')).toBe('xml');
		expect(detectLanguage('page', '<!DOCTYPE html>\n<html></html>')).toBe('html');
		expect(
			detectLanguage('image', '# syntax=docker/dockerfile:1\nFROM alpine\nRUN true\n')
		).toBe('dockerfile');
		expect(detectLanguage('sites-enabled/default', 'server {\n  listen 80;\n}\n')).toBe(
			'nginx'
		);
		expect(detectLanguage('site.conf', 'server {\n  listen 80;\n}\n')).toBe('nginx');
		expect(detectLanguage('app.conf', 'port = 80\n')).toBe('properties');
		expect(detectLanguage('changes', 'diff --git a/x b/x\n--- a/x\n+++ b/x\n')).toBe('diff');
		expect(detectLanguage('config', 'services:\n  web:\n    image: nginx\n')).toBe('yaml');
		expect(detectLanguage('vars', '# comment\nFOO=bar\nexport BAZ=1\n')).toBe('properties');
		expect(detectLanguage('settings', '[main]\nname = x\n')).toBe('properties');
		expect(detectLanguage('README', '# Silo\n\nSee [docs](https://x.test).\n')).toBe(
			'markdown'
		);
		expect(detectLanguage('LICENSE', 'MIT License\n\nCopyright (c) 2026\n')).toBe('text');
		expect(detectLanguage('empty', '')).toBe('text');
		// Many leading comments stay cheap (no exponential backtracking).
		expect(detectLanguage('page', '<!-- c -->\n'.repeat(3) + '<html>')).toBe('html');
		expect(detectLanguage('page', '<!-- c -->\n'.repeat(60) + '<div>')).toBe('text');
	});

	it('maps fence names and extensions to languages', () => {
		expect(languageByName('bash')).toBe('shell');
		expect(languageByName('YML')).toBe('yaml');
		expect(languageByName('c++')).toBe('cpp');
		expect(languageByName('typescript')).toBe('typescript');
		expect(languageByName('brainfuck')).toBeNull();
	});
});

describe('Auto Detect', () => {
	it('is the first option and names what it found', () => {
		const options = languageOptions('shell');
		expect(options[0]).toEqual({ value: 'auto', label: 'Auto Detect (Shell)' });
		expect(options.at(-1)).toEqual({ value: 'text', label: 'Plain Text' });
		const named = options.slice(1, -1).map((o) => o.label);
		expect(named).toEqual([...named].sort((a, b) => a.localeCompare(b)));
	});

	it('follows the text until the user picks a language', () => {
		const tab = { path: 'entrypoint', language: 'auto' as const, buffer: '#!/bin/bash\n' };
		expect(tabLanguage(tab)).toBe('shell');
		expect(tabLanguage({ ...tab, buffer: '' })).toBe('text');
		expect(tabLanguage({ ...tab, language: 'python' })).toBe('python');
	});
});
