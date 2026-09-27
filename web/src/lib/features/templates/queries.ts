// Template queries (template registry): the instance's own templates, one
// template and its versions. Keys follow the live conventions (liveKeys),
// so template changes (the `templates` topic) refresh them.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';

export type Template = Schema<'Template'>;
export type TemplateVersion = Schema<'TemplateVersion'>;
export type TemplateIconInfo = Schema<'TemplateIconInfo'>;
export type TemplateDefinition = Schema<'TemplateDefinition'>;
export type TemplateIconMap = Schema<'TemplateIconMap'>;

export const templateKeys = {
	all: ['templates'] as const,
	list: () => liveKeys.list('templates', 'own'),
	detail: (id: string) => liveKeys.item('templates', id),
	versions: (id: string) => liveKeys.item('templates', id, 'versions'),
	definition: (id: string, version: number) =>
		liveKeys.item('templates', id, 'definition', String(version)),
	icons: () => liveKeys.list('templates', 'icons')
};

/**
 * The current icon of every template by registry and template (stacks
 * created from a template show it; refreshed on template changes).
 */
export function templateIconsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: templateKeys.icons(),
		queryFn: ({ signal }) => unwrap(client.GET('/api/v1/template-icons', { signal })),
		staleTime: 60_000
	});
}

/** The icon URL of a stack's template, if it has one. */
export function templateIconUrl(
	map: TemplateIconMap | undefined,
	ref: { instanceId: string; templateId: string } | undefined | null
): string | undefined {
	if (!map || !ref) return undefined;
	return map.items.find((i) => i.instanceId === ref.instanceId && i.templateId === ref.templateId)
		?.url;
}

/** A version's Compose files and .env (needs template.use). */
export function templateDefinitionQuery(id: string, version: number, client: ApiClient = api) {
	return queryOptions({
		queryKey: templateKeys.definition(id, version),
		queryFn: ({ signal }) =>
			unwrap(
				client.GET('/api/v1/templates/{templateId}/versions/{version}/definition', {
					params: { path: { templateId: id, version } },
					signal
				})
			),
		enabled: !!id && version > 0,
		retry: false,
		staleTime: Infinity
	});
}

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

// Registries and the catalog of every registry ----------------------------

export type TemplateRegistryInfo = Schema<'TemplateRegistryInfo'>;
export type TemplateCatalogItem = Schema<'TemplateCatalogItem'>;
export type RegistryDefinition = Schema<'RegistryDefinition'>;

export const catalogKeys = {
	registries: () => liveKeys.list('templates', 'registries'),
	catalog: () => liveKeys.list('templates', 'catalog'),
	item: (instanceId: string, templateId: string) =>
		liveKeys.item('templates', 'catalog', instanceId, templateId),
	definition: (instanceId: string, templateId: string, version: number) =>
		liveKeys.item('templates', 'catalog', instanceId, templateId, String(version))
};

/** This instance's registry (first) and the added registries. */
export function templateRegistriesQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: catalogKeys.registries(),
		queryFn: async ({ signal }) =>
			(await unwrap(client.GET('/api/v1/template-registries', { signal }))).items,
		staleTime: 10_000
	});
}

/** Templates of every registry the caller may browse. */
export function templateCatalogQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: catalogKeys.catalog(),
		queryFn: async ({ signal }) =>
			(await unwrap(client.GET('/api/v1/template-catalog', { signal }))).items,
		staleTime: 10_000
	});
}

/** One template of an added registry. */
export function catalogItemQuery(instanceId: string, templateId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: catalogKeys.item(instanceId, templateId),
		queryFn: ({ signal }) =>
			unwrap(
				client.GET('/api/v1/template-catalog/{instanceId}/{templateId}', {
					params: { path: { instanceId, templateId } },
					signal
				})
			),
		enabled: !!instanceId && !!templateId,
		retry: false
	});
}

/** A registry template version's Compose files (downloaded from its registry). */
export function catalogDefinitionQuery(
	instanceId: string,
	templateId: string,
	version: number,
	client: ApiClient = api
) {
	return queryOptions({
		queryKey: catalogKeys.definition(instanceId, templateId, version),
		queryFn: ({ signal }) =>
			unwrap(
				client.GET(
					'/api/v1/template-catalog/{instanceId}/{templateId}/versions/{version}/definition',
					{ params: { path: { instanceId, templateId, version } }, signal }
				)
			),
		enabled: !!instanceId && !!templateId && version > 0,
		retry: false,
		staleTime: Infinity
	});
}
