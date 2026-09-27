// Backups (#10) for Svelte Query. Repositories, snapshots and restores are
// live topic 'backups'; backup policies are topic 'policies'.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import { fetchAllPages } from '$lib/features/common/data';
import type {
	Backup,
	BackupDetail,
	BackupPolicy,
	BackupRepository,
	RepositoryHealth
} from './model';

export interface BackupFilter {
	repositoryId?: string;
	policyId?: string;
	setId?: string;
	environmentId?: string;
	stackId?: string;
	kind?: 'manager_state' | 'stack' | 'volume';
	/** Backups of a volume: its own and the stack backups that hold it. */
	volume?: string;
}

export const backupKeys = {
	repositories: () => liveKeys.list('backups', 'repositories'),
	repository: (id: string) => liveKeys.item('backups', id),
	health: (id: string) => liveKeys.item('backups', id, 'health'),
	policies: () => liveKeys.list('policies', 'backups'),
	policy: (id: string) => liveKeys.item('policies', id),
	backups: (f: BackupFilter) => liveKeys.list('backups', 'snapshots', f),
	backup: (id: string) => liveKeys.item('backups', id),
	contents: (id: string, path: string) => liveKeys.item('backups', id, 'contents', path)
};

export function repositoriesQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: backupKeys.repositories(),
		queryFn: ({ signal }): Promise<BackupRepository[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/backup-repositories', {
						params: { query: { cursor, limit: 200 } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

export function repositoryQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: backupKeys.repository(id),
		queryFn: ({ signal }): Promise<BackupRepository> =>
			unwrap(
				client.GET('/api/v1/backup-repositories/{repositoryId}', {
					params: { path: { repositoryId: id } },
					signal
				})
			)
	});
}

export function repositoryHealthQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: backupKeys.health(id),
		queryFn: ({ signal }): Promise<RepositoryHealth> =>
			unwrap(
				client.GET('/api/v1/backup-repositories/{repositoryId}/health', {
					params: { path: { repositoryId: id } },
					signal
				})
			),
		retry: false
	});
}

export function backupPoliciesQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: backupKeys.policies(),
		queryFn: ({ signal }): Promise<BackupPolicy[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/backup-policies', {
						params: { query: { cursor, limit: 200 } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

/**
 * Every policy with its recent sets (the list omits them): the backup
 * history overview. One detail request per policy the caller can read.
 */
export function backupPoliciesWithSetsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('policies', 'backups', 'with-sets'),
		queryFn: async ({ signal }): Promise<BackupPolicy[]> => {
			const list = await fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/backup-policies', {
						params: { query: { cursor, limit: 200 } },
						signal
					})
				)
			);
			return Promise.all(
				list.map((p) =>
					p.view === 'full'
						? unwrap(
								client.GET('/api/v1/backup-policies/{policyId}', {
									params: { path: { policyId: p.id } },
									signal
								})
							)
						: p
				)
			);
		},
		staleTime: 15_000
	});
}

export function backupPolicyQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: backupKeys.policy(id),
		queryFn: ({ signal }): Promise<BackupPolicy> =>
			unwrap(
				client.GET('/api/v1/backup-policies/{policyId}', {
					params: { path: { policyId: id } },
					signal
				})
			)
	});
}

export function backupsQuery(filter: BackupFilter = {}, client: ApiClient = api) {
	return queryOptions({
		queryKey: backupKeys.backups(filter),
		queryFn: ({ signal }): Promise<Backup[]> =>
			fetchAllPages(
				(cursor) =>
					unwrap(
						client.GET('/api/v1/backups', {
							params: { query: { cursor, limit: 200, ...filter } },
							signal
						})
					),
				5
			),
		staleTime: 15_000
	});
}

export function backupQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: backupKeys.backup(id),
		queryFn: ({ signal }): Promise<BackupDetail> =>
			unwrap(
				client.GET('/api/v1/backups/{backupId}', {
					params: { path: { backupId: id } },
					signal
				})
			)
	});
}

/**
 * One directory of a backup. restic lists the directory itself too; it is
 * dropped here as well as on the server, so no view can open a folder
 * inside itself.
 */
export function backupContentsQuery(id: string, path: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: backupKeys.contents(id, path),
		queryFn: async ({ signal }) => {
			const c = await unwrap(
				client.GET('/api/v1/backups/{backupId}/contents', {
					params: { path: { backupId: id }, query: { path, limit: 500 } },
					signal
				})
			);
			return { ...c, entries: c.entries.filter((e) => e.path !== path) };
		},
		staleTime: 5 * 60_000,
		retry: false
	});
}
