import { describe, expect, it } from 'vitest';
import { checkLabel, maskFingerprint } from '$lib/features/registries/model';
import {
	emptyForm,
	formFromSource,
	isComplete,
	repoLabel,
	toSource,
	validateSource
} from './source';

describe('Git build form (#33)', () => {
	const form = () => ({
		...emptyForm(),
		gitUrl: ' https://github.com/acme/app.git ',
		ref: 'main',
		tags: 'registry.example.com/acme/app:1.4\n\nghcr.io/acme/app:latest',
		buildArgs: 'NODE_VERSION=22\n# comment\nAPI_URL=http://api:8080'
	});

	it('builds the API source from the form', () => {
		expect(toSource(form())).toEqual({
			gitUrl: 'https://github.com/acme/app.git',
			ref: 'main',
			contextPath: undefined,
			dockerfile: undefined,
			target: undefined,
			buildArgs: { NODE_VERSION: '22', API_URL: 'http://api:8080' },
			tags: ['registry.example.com/acme/app:1.4', 'ghcr.io/acme/app:latest'],
			platform: undefined,
			noCache: undefined,
			pull: undefined,
			gitCredentialId: undefined
		});
		expect(isComplete(form())).toBe(true);
		expect(isComplete({ ...form(), tags: '' })).toBe(false);
		expect(validateSource(form())).toEqual({});
	});

	it('round-trips a saved definition', () => {
		const src = toSource({ ...form(), noCache: true, target: 'runtime' });
		expect(toSource(formFromSource(src))).toEqual(src);
		expect(formFromSource(undefined)).toEqual(emptyForm());
	});

	it('refuses SSH URLs, credentials in URLs, bad names, bad argument keys and escaping Dockerfiles', () => {
		expect(
			validateSource({ ...form(), gitUrl: 'git@github.com:acme/app.git' }).gitUrl
		).toContain('https://');
		expect(
			validateSource({ ...form(), gitUrl: 'https://user:tok@github.com/a/b' }).gitUrl
		).toContain('credentials');
		expect(validateSource({ ...form(), tags: 'Acme/App:1' }).tags).toContain('Acme/App:1');
		expect(validateSource({ ...form(), buildArgs: '1BAD=x' }).buildArgs).toContain('1BAD');
		expect(validateSource({ ...form(), buildArgs: 'novalue' }).buildArgs).toContain('Line 1');
		expect(validateSource({ ...form(), dockerfile: '../Dockerfile' }).dockerfile).toBeDefined();
		expect(validateSource({ ...form(), tags: 'localhost:5000/app:dev' })).toEqual({});
	});

	it('labels repositories without scheme and .git', () => {
		expect(repoLabel('https://github.com/silo/web.git')).toBe('github.com/silo/web');
	});
});

describe('credential display (#19)', () => {
	it('masks fingerprints and names check results', () => {
		expect(maskFingerprint('fp_3f2a9c0d1e4b5a67')).toBe('fp_3f2a…5a67');
		expect(maskFingerprint('short')).toBe('short');
		expect(maskFingerprint(undefined)).toBe('—');
		expect(checkLabel('rate_limited')).toBe('Rate limited');
		expect(checkLabel('something_new')).toBe('something new');
	});
});
