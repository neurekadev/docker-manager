// Template queries (template registry): the instance's own templates, one
// template and its versions. Keys follow the live conventions (liveKeys),
// so template changes (the `templates` topic) refresh them.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';

export type Template = Schema<'Template'>;
export type TemplateVersion = Schema<'TemplateVersion'>;
export type TemplateIconInfo = Schema<'TemplateIconInfo'>;

export const templateKeys = {
	all: ['templates'] as const,
	list: () => liveKeys.list('templates', 'own'),
	detail: (id: string) => liveKeys.item('templates', id),
	versions: (id: string) => liveKeys.item('templates', id, 'versions')
};

/** Every template the caller can see (all pages; templates are few). */
export function templatesQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: templateKeys.list(),
		queryFn: async ({ signal }) => {
			const out: Template[] = [];
			let cursor: string | undefined;
			for (let i = 0; i < 50; i++) {
				const p = await unwrap(
					client.GET('/api/v1/templates', {
						params: { query: { limit: 200, cursor } },
						signal
					})
				);
				out.push(...p.items);
				cursor = p.nextCursor || undefined;
				if (!cursor) break;
			}
			return out;
		},
		staleTime: 10_000
	});
}

export function templateQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: templateKeys.detail(id),
		queryFn: ({ signal }) =>
			unwrap(
				client.GET('/api/v1/templates/{templateId}', {
					params: { path: { templateId: id } },
					signal
				})
			),
		enabled: !!id,
		retry: false
	});
}

export function templateVersionsQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: templateKeys.versions(id),
		queryFn: async ({ signal }) =>
			(
				await unwrap(
					client.GET('/api/v1/templates/{templateId}/versions', {
						params: { path: { templateId: id } },
						signal
					})
				)
			).items,
		enabled: !!id
	});
}
