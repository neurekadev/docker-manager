// Access administration (#16, #17, #31) for Svelte Query. Users, groups,
// invitations, permission documents, API tokens and signed-in devices
// (browser sessions) are live topic
// 'permissions': any change refreshes these keys (and a permissions change
// of the caller clears the cache, #23).
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type Account, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import { fetchAllPages } from '$lib/features/common/data';
import type { Catalog } from './permissions';

export type Group = Schema<'Group'>;
export type Invitation = Schema<'Invitation'>;
export type PermissionDocument = Schema<'PermissionDocument'>;
export type EffectivePermissions = Schema<'EffectivePermissions'>;
export type APIToken = Schema<'APIToken'>;
export type UserSession = Schema<'UserSession'>;

export const accessKeys = {
	users: () => liveKeys.list('permissions', 'users'),
	user: (id: string) => liveKeys.item('permissions', id),
	userRules: (id: string) => liveKeys.item('permissions', id, 'rules'),
	effective: (id: string) => liveKeys.item('permissions', id, 'effective'),
	groups: () => liveKeys.list('permissions', 'groups'),
	groupRules: (id: string) => liveKeys.item('permissions', id, 'rules'),
	invitations: () => liveKeys.list('permissions', 'invitations'),
	myTokens: () => liveKeys.list('permissions', 'my-tokens'),
	allTokens: () => liveKeys.list('permissions', 'all-tokens'),
	mySessions: () => liveKeys.list('permissions', 'my-sessions'),
	userSessions: (id: string) => liveKeys.item('permissions', id, 'sessions'),
	catalog: ['permission-catalog'] as const
};

export function catalogQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.catalog,
		queryFn: ({ signal }): Promise<Catalog> =>
			unwrap(client.GET('/api/v1/permission-catalog', { signal })),
		staleTime: 10 * 60_000
	});
}

export function usersQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.users(),
		queryFn: ({ signal }): Promise<Account[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/users', {
						params: { query: { cursor, limit: 200 } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

export function userQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.user(id),
		queryFn: ({ signal }): Promise<Account> =>
			unwrap(
				client.GET('/api/v1/users/{userId}', { params: { path: { userId: id } }, signal })
			)
	});
}

export function userRulesQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.userRules(id),
		queryFn: ({ signal }): Promise<PermissionDocument> =>
			unwrap(
				client.GET('/api/v1/users/{userId}/permissions', {
					params: { path: { userId: id } },
					signal
				})
			)
	});
}

export function effectiveQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.effective(id),
		queryFn: ({ signal }): Promise<EffectivePermissions> =>
			unwrap(
				client.GET('/api/v1/users/{userId}/effective-permissions', {
					params: { path: { userId: id } },
					signal
				})
			)
	});
}

export function groupsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.groups(),
		queryFn: async ({ signal }): Promise<Group[]> =>
			(await unwrap(client.GET('/api/v1/groups', { signal }))).items,
		staleTime: 15_000
	});
}

export function groupRulesQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.groupRules(id),
		queryFn: ({ signal }): Promise<PermissionDocument> =>
			unwrap(
				client.GET('/api/v1/groups/{groupId}/permissions', {
					params: { path: { groupId: id } },
					signal
				})
			),
		enabled: !!id
	});
}

export function invitationsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.invitations(),
		queryFn: ({ signal }): Promise<Invitation[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/invitations', {
						params: { query: { cursor, limit: 200 } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

export function myTokensQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.myTokens(),
		queryFn: ({ signal }): Promise<APIToken[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/me/api-tokens', {
						params: { query: { cursor, limit: 200 } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

export function allTokensQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.allTokens(),
		queryFn: ({ signal }): Promise<APIToken[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/api-tokens', {
						params: { query: { cursor, limit: 200 } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

/** My signed-in devices, most recently active first (one short page). */
export function mySessionsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.mySessions(),
		queryFn: async ({ signal }): Promise<UserSession[]> =>
			(await unwrap(client.GET('/api/v1/me/sessions', { signal }))).items,
		staleTime: 15_000
	});
}

/** A user's signed-in devices (owner only), most recently active first. */
export function userSessionsQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: accessKeys.userSessions(id),
		queryFn: async ({ signal }): Promise<UserSession[]> =>
			(
				await unwrap(
					client.GET('/api/v1/users/{userId}/sessions', {
						params: { path: { userId: id } },
						signal
					})
				)
			).items,
		staleTime: 15_000,
		enabled: !!id
	});
}
