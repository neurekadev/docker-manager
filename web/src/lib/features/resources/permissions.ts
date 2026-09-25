// Permission-aware rendering for the resource pages (#17, #22): actions on
// an object come from its DTO `actions`; creating something in an
// environment has no object yet, so it is read from /me/permissions (an
// allowed entry at instance scope or on that environment). Hidden, not
// disabled; the server still decides every request.
import type { MyPermissions } from '$lib/api/client';

/** The object's DTO grants the capability. */
export function can(actions: readonly string[] | undefined | null, capability: string): boolean {
	return !!actions && actions.includes(capability);
}

/** The caller may use an environment-scoped capability in env. */
export function canInEnvironment(
	perms: MyPermissions | undefined | null,
	capability: string,
	env: string
): boolean {
	if (!perms) return false;
	if (perms.owner) return true;
	return perms.entries.some(
		(e) =>
			e.allowed &&
			e.capability === capability &&
			(e.scope.kind === 'instance' ||
				(e.scope.kind === 'environment' && e.scope.environmentId === env))
	);
}

/** The environments (of those given) where the capability is allowed. */
export function environmentsAllowing<T extends { id: string }>(
	perms: MyPermissions | undefined | null,
	capability: string,
	envs: readonly T[]
): T[] {
	return envs.filter((e) => canInEnvironment(perms, capability, e.id));
}
