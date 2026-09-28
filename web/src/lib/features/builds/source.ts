// The manual Git build form's model (#33): text fields in, the API's
// BuildSource out, with the checks the form shows inline. Unit-tested in
// source.spec.ts.
import type { Schema } from '$lib/api/client';
import { lines, parsePairs } from '$lib/features/resources/model';

export type BuildSource = Schema<'BuildSource'>;
type ImageBuild = Schema<'ImageBuild'>;

export interface SourceForm {
	gitUrl: string;
	ref: string;
	contextPath: string;
	dockerfile: string;
	target: string;
	/** KEY=value lines. */
	buildArgs: string;
	/** name:tag lines. */
	tags: string;
	platform: string;
	noCache: boolean;
	pull: boolean;
	gitCredentialId: string;
}

export function emptyForm(): SourceForm {
	return {
		gitUrl: '',
		ref: '',
		contextPath: '',
		dockerfile: '',
		target: '',
		buildArgs: '',
		tags: '',
		platform: '',
		noCache: false,
		pull: false,
		gitCredentialId: ''
	};
}

export function formFromSource(s: BuildSource | undefined): SourceForm {
	if (!s) return emptyForm();
	return {
		gitUrl: s.gitUrl,
		ref: s.ref ?? '',
		contextPath: s.contextPath ?? '',
		dockerfile: s.dockerfile ?? '',
		target: s.target ?? '',
		buildArgs: Object.entries(s.buildArgs ?? {})
			.map(([k, v]) => `${k}=${v}`)
			.join('\n'),
		tags: (s.tags ?? []).join('\n'),
		platform: s.platform ?? '',
		noCache: !!s.noCache,
		pull: !!s.pull,
		gitCredentialId: s.gitCredentialId ?? ''
	};
}

export type SourceErrors = Partial<Record<'gitUrl' | 'tags' | 'buildArgs' | 'dockerfile', string>>;

const ARG_KEY = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;
const TAG =
	/^[a-z0-9]+([._-][a-z0-9]+)*(:\d+)?(\/[a-z0-9]+([._-][a-z0-9]+)*)*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?$/;

/** Inline checks (the server validates again and resolves the ref). */
export function validateSource(f: SourceForm): SourceErrors {
	const e: SourceErrors = {};
	const url = f.gitUrl.trim();
	if (url && !/^https?:\/\/[^\s]+$/.test(url))
		e.gitUrl = 'Use an https:// repository URL. SSH Git URLs are not supported.';
	else if (/^https?:\/\/[^/]*@/.test(url))
		e.gitUrl = 'Leave credentials out of the URL; pick a Git credential instead.';
	const tags = lines(f.tags);
	const bad = tags.filter((t) => !TAG.test(t));
	if (bad.length) e.tags = `Not an image name: ${bad.join(', ')}. Use lowercase name:tag.`;
	const args = parsePairs(f.buildArgs);
	if (args.invalid.length) e.buildArgs = `Line ${args.invalid.join(', ')}: use KEY=value.`;
	else {
		const keys = Object.keys(args.values).filter((k) => !ARG_KEY.test(k));
		if (keys.length) e.buildArgs = `Not a build argument name: ${keys.join(', ')}.`;
	}
	if (f.dockerfile.startsWith('/') || f.dockerfile.includes('..'))
		e.dockerfile = 'Use a path relative to the build context.';
	return e;
}

/** The required fields are filled in (a URL and at least one image name). */
export function isComplete(f: SourceForm): boolean {
	return !!f.gitUrl.trim() && lines(f.tags).length > 0;
}

/** The API body (BuildSource) of a valid form. */
export function toSource(f: SourceForm): BuildSource {
	const args = parsePairs(f.buildArgs).values;
	return {
		gitUrl: f.gitUrl.trim(),
		ref: f.ref.trim() || undefined,
		contextPath: f.contextPath.trim() || undefined,
		dockerfile: f.dockerfile.trim() || undefined,
		target: f.target.trim() || undefined,
		buildArgs: Object.keys(args).length ? args : undefined,
		tags: lines(f.tags),
		platform: f.platform.trim() || undefined,
		noCache: f.noCache || undefined,
		pull: f.pull || undefined,
		gitCredentialId: f.gitCredentialId || undefined
	};
}

/** "github.com/silo/web" from "https://github.com/silo/web.git". */
export function repoLabel(url: string): string {
	return url.replace(/^https?:\/\//, '').replace(/\.git$/, '');
}

/**
 * The source of an earlier build, to build it again. The build record keeps
 * the names of its build arguments only (never their values), so they come
 * back empty ("NAME=") and `argsMissing` says the user must fill them in.
 */
export function sourceOfBuild(b: ImageBuild): { source: BuildSource; argsMissing: boolean } {
	const argsMissing = b.buildArgNames.length > 0;
	return {
		source: {
			gitUrl: b.gitUrl,
			ref: b.ref || undefined,
			contextPath: b.contextPath || undefined,
			dockerfile: b.dockerfile || undefined,
			target: b.target || undefined,
			buildArgs: argsMissing
				? Object.fromEntries(b.buildArgNames.map((n) => [n, '']))
				: undefined,
			tags: [...b.tags],
			platform: b.platform || undefined,
			noCache: b.noCache || undefined,
			pull: b.pull || undefined,
			gitCredentialId: b.gitCredentialId || undefined
		},
		argsMissing
	};
}

/** "16:54:03": the time of a log line (24 h, the viewer's zone unless given). */
export function clockTime(iso: string, timeZone?: string): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return '';
	return new Intl.DateTimeFormat('en', {
		hour: '2-digit',
		minute: '2-digit',
		second: '2-digit',
		hourCycle: 'h23',
		timeZone
	}).format(d);
}
