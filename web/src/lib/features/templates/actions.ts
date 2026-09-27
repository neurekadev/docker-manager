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
