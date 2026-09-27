// Template mutations (template registry). Edits send If-Match with the
// template's revision; publishing and visibility changes of public
// templates carry the explicit acknowledgement that every file, .env
// included, becomes public.
import { api, unwrap, type ApiClient } from '$lib/api/client';
import type { Template, TemplateVersion } from './queries';

const ifMatch = (t: Template) => ({ 'If-Match': `"${t.revision ?? 0}"` });
const path = (t: Pick<Template, 'id'>) => ({ templateId: t.id });

export function createTemplate(
	body: { name: string; description?: string; tags?: string[] },
	client: ApiClient = api
): Promise<Template> {
	return unwrap(client.POST('/api/v1/templates', { body }));
}

export function patchTemplate(
	t: Template,
	body: { name?: string; description?: string; tags?: string[] },
	client: ApiClient = api
): Promise<Template> {
	return unwrap(
		client.PATCH('/api/v1/templates/{templateId}', {
			params: { path: path(t), header: ifMatch(t) },
			body
		})
	);
}

export function setVisibility(
	t: Template,
	visibility: 'private' | 'public',
	acknowledgePublic: boolean,
	client: ApiClient = api
): Promise<Template> {
	return unwrap(
		client.PUT('/api/v1/templates/{templateId}/visibility', {
			params: { path: path(t), header: ifMatch(t) },
			body: { visibility, acknowledgePublic }
		})
	);
}

export async function deleteTemplate(t: Template, client: ApiClient = api): Promise<void> {
	await unwrap(
		client.DELETE('/api/v1/templates/{templateId}', {
			params: { path: path(t), header: ifMatch(t) }
		})
	);
}

/** Uploads icon bytes (the server detects the image type). */
export function setIcon(t: Template, data: string, client: ApiClient = api): Promise<Template> {
	return unwrap(
		client.PUT('/api/v1/templates/{templateId}/icon', {
			params: { path: path(t) },
			body: { data }
		})
	);
}

export function removeIcon(t: Template, client: ApiClient = api): Promise<Template> {
	return unwrap(
		client.DELETE('/api/v1/templates/{templateId}/icon', { params: { path: path(t) } })
	);
}

export function publishVersion(
	t: Template,
	body: { label: string; notes?: string; acknowledgePublic?: boolean },
	client: ApiClient = api
): Promise<TemplateVersion> {
	return unwrap(
		client.POST('/api/v1/templates/{templateId}/versions', { params: { path: path(t) }, body })
	);
}

export async function deleteVersion(
	t: Template,
	version: number,
	client: ApiClient = api
): Promise<void> {
	await unwrap(
		client.DELETE('/api/v1/templates/{templateId}/versions/{version}', {
			params: { path: { templateId: t.id, version } }
		})
	);
}

/** Creates a stack from a published template version (nothing is deployed). */
export function createStackFromTemplate(
	body: {
		environmentId: string;
		name: string;
		displayName?: string;
		description?: string;
		/** The registry (instance ID); empty: this instance. */
		instanceId?: string;
		templateId: string;
		version: number;
	},
	client: ApiClient = api
) {
	return unwrap(client.POST('/api/v1/stacks/template-creations', { body }));
}

/** Reads a picked file as base64 (the icon upload body). */
export function fileToBase64(file: Blob): Promise<string> {
	return new Promise((resolve, reject) => {
		const r = new FileReader();
		r.onload = () => {
			const s = String(r.result ?? '');
			resolve(s.slice(s.indexOf(',') + 1));
		};
		r.onerror = () => reject(r.error ?? new Error('The file could not be read.'));
		r.readAsDataURL(file);
	});
}

/** Adds another instance's registry (owner). */
export function addRegistry(url: string, client: ApiClient = api) {
	return unwrap(client.POST('/api/v1/template-registries', { body: { url } }));
}

/** Removes an added registry (owner). */
export async function removeRegistry(instanceId: string, client: ApiClient = api): Promise<void> {
	await unwrap(
		client.DELETE('/api/v1/template-registries/{instanceId}', {
			params: { path: { instanceId } }
		})
	);
}

/** Syncs an added registry now (owner). */
export function syncRegistry(instanceId: string, client: ApiClient = api) {
	return unwrap(
		client.POST('/api/v1/template-registries/{instanceId}/syncs', {
			params: { path: { instanceId } }
		})
	);
}

/** Saves a stack's files as a new template, or as an existing template's draft. */
export function saveStackAsTemplate(
	body: {
		stackId: string;
		paths?: string[];
		templateId?: string;
		name?: string;
		description?: string;
	},
	client: ApiClient = api
): Promise<Template> {
	return unwrap(client.POST('/api/v1/templates/stack-imports', { body }));
}

/** Copies a published version into a new private template. */
export function duplicateTemplate(
	body: { name: string; instanceId?: string; templateId: string; version: number },
	client: ApiClient = api
): Promise<Template> {
	return unwrap(client.POST('/api/v1/templates/duplicates', { body }));
}

/** Replaces a template's draft with one of its versions. */
export function restoreDraft(t: Template, version: number, client: ApiClient = api) {
	return unwrap(
		client.POST('/api/v1/templates/{templateId}/draft-restores', {
			params: { path: path(t) },
			body: { version }
		})
	);
}
