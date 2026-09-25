// Permission helpers for rendering (#17): what to show, never what is
// allowed (the server decides every request). Resource DTOs carry their
// own `actions`; creation and instance-wide actions read the caller's
// effective permissions (GET /me/permissions, accessOf in $lib/shell/nav).
import { ApiRequestError } from '$lib/api/client';
import type { Access } from '$lib/shell/nav';

/** The caller holds the capability on at least one scope (or is the owner). */
export function can(a: Access, capability: string): boolean {
	return a.owner || a.allowed.has(capability);
}

/** A resource DTO grants the action. */
export function has(resource: { actions?: readonly string[] } | undefined | null, key: string) {
	return !!resource?.actions?.includes(key);
}

/** The request was refused for lack of permission (show the denied state). */
export function isDenied(e: unknown): boolean {
	return e instanceof ApiRequestError && e.status === 403;
}

/** The resource does not exist or is hidden from the caller. */
export function isNotFound(e: unknown): boolean {
	return e instanceof ApiRequestError && e.status === 404;
}
