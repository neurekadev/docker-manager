// The environments the caller sees, for lists and forms that adapt to a
// single environment (#22 polish): with exactly one, lists hide their
// Environment column and filter and forms their environment picker.
// Reads the shared environments query (the switcher's), so it adds no
// request. Call from a component's script.
import { createQuery } from '@tanstack/svelte-query';
import { environmentsQuery } from '$lib/api/queries';
import { onlyOneEnvironment } from './data';

export function singleEnvironment() {
	const envs = createQuery(() => environmentsQuery());
	const only = $derived(onlyOneEnvironment(envs.data));
	return {
		/** True when the caller sees exactly one (non-archived) environment. */
		get current(): boolean {
			return only;
		},
		/** That environment, when there is exactly one. */
		get environment() {
			return only ? (envs.data?.find((e) => !e.archivedAt) ?? null) : null;
		}
	};
}
