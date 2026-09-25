// Registry connection and Git credential helpers (#19, #33).
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient } from '$lib/api/client';
import { allPages } from '$lib/api/multi-env';

/** "fp_3f2a9c0d1e4b5a67" → "fp_3f2a…5a67": enough to compare, not the whole value. */
export function maskFingerprint(fp: string | undefined): string {
	if (!fp) return '—';
	const [prefix, rest] = fp.includes('_')
		? [fp.slice(0, fp.indexOf('_') + 1), fp.slice(fp.indexOf('_') + 1)]
		: ['', fp];
	if (rest.length <= 8) return fp;
	return `${prefix}${rest.slice(0, 4)}…${rest.slice(-4)}`;
}

const CHECKS: Record<string, string> = {
	ok: 'Works',
	unauthorized: 'Login refused',
	forbidden: 'No access',
	not_found: 'Not found',
	rate_limited: 'Rate limited',
	registry_unavailable: 'Unreachable',
	git_unavailable: 'Unreachable',
	platform_not_found: 'Platform missing',
	invalid_response: 'Bad answer',
	ref_not_found: 'Ref not found',
	invalid_git_url: 'Bad URL'
};

/** Short label of a last check result (error classes of #19). */
export function checkLabel(result: string): string {
	return CHECKS[result] ?? result.replaceAll('_', ' ');
}

/** Stack names for binding pickers and labels (id → name). */
export function stackNamesQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: ['stacks', 'list', 'names'] as const,
		queryFn: async ({ signal }) => {
			const all = await allPages((cursor) =>
				unwrap(
					client.GET('/api/v1/stacks', {
						params: { query: { limit: 200, cursor } },
						signal
					})
				)
			);
			return all.map((s) => ({
				id: s.id,
				name: s.displayName || s.name,
				environmentId: s.environmentId
			}));
		},
		staleTime: 60_000,
		retry: false
	});
}
