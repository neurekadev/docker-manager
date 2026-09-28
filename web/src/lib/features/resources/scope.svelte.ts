// The environments a resource page reads (#22 environment switcher): the
// selected one or every visible active one, their names for messages and
// the caller's permissions for create actions. With a single environment
// (selected, or the only one) lists hide their Environment column. Call
// from a component's script (it creates queries).
import { createQuery } from '@tanstack/svelte-query';
import { envTargets, type EnvTarget } from '$lib/api/multi-env';
import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
import { environmentSelection } from '$lib/shell/environment.svelte';
import { onlyOneEnvironment } from '$lib/features/common/data';
import { accessOf, hasAny, isRestricted } from '$lib/shell/nav';
import { canInEnvironment, environmentsAllowing } from './permissions';

export function useEnvironmentScope() {
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const targets = $derived<EnvTarget[]>(envTargets(envs.data ?? [], environmentSelection.id));
	const access = $derived(accessOf(perms.data));

	return {
		envs,
		perms,
		get targets() {
			return targets;
		},
		/**
		 * One environment is shown: selected in the switcher, or the only
		 * one the caller sees (lists then hide their Environment column
		 * and filter, forms their picker).
		 */
		get single() {
			return (
				targets.length === 1 &&
				(environmentSelection.id !== null || onlyOneEnvironment(envs.data))
			);
		},
		get ready() {
			return envs.isSuccess;
		},
		get restricted() {
			return perms.data ? isRestricted(access) : false;
		},
		/** The caller holds any capability with one of the prefixes. */
		hasAny(...prefixes: string[]) {
			return hasAny(access, ...prefixes);
		},
		name(id: string): string {
			return envs.data?.find((e) => e.id === id)?.name ?? id;
		},
		environment(id: string) {
			return envs.data?.find((e) => e.id === id);
		},
		/** Online target environments where `capability` is allowed (create forms). */
		creatable(capability: string): EnvTarget[] {
			return environmentsAllowing(
				perms.data,
				capability,
				targets.filter((t) => t.online)
			);
		},
		can(capability: string, env: string) {
			return canInEnvironment(perms.data, capability, env);
		}
	};
}

export type EnvironmentScope = ReturnType<typeof useEnvironmentScope>;
