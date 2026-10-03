// A backup in the shared file picker (#10, FilePicker.svelte): its places
// (the stack's project directory and each volume, or one volume) and its
// folders, listed one per request (at most 500 entries each).
import HardDrive from '@lucide/svelte/icons/hard-drive';
import Layers from '@lucide/svelte/icons/layers';
import { api, unwrap, type ApiClient } from '$lib/api/client';
import type { PickerEntry, PickerPlace, PickerSource } from '$lib/features/common/filePicker';
import type { BackupNode } from './model';
import { backupKeys } from './queries';

/** Entries listed per folder. */
export const PICKER_LIMIT = 500;

/**
 * The places of a backup: the stack's project directory and its volumes,
 * or only `volume` (a volume's page); the whole backup when it records
 * neither.
 */
export function backupPlaces(
	b: {
		kind?: string;
		stackName?: string;
		volume?: string;
		projectPath?: string;
		volumePaths?: Record<string, string>;
		paths?: string[];
	},
	volume?: string
): PickerPlace[] {
	const vols = Object.entries(b.volumePaths ?? {}).sort(([a], [c]) => a.localeCompare(c));
	const out: PickerPlace[] = [];
	if (!volume && b.kind === 'stack' && b.projectPath)
		out.push({
			path: b.projectPath,
			label: `${b.stackName ?? 'Stack'} Project Files`,
			icon: Layers
		});
	for (const [name, path] of vols) {
		if (!volume || name === volume)
			out.push({ path, label: `Volume ${name}`, icon: HardDrive });
	}
	if (!out.length && b.kind === 'volume' && b.paths?.length) {
		out.push({ path: b.paths[0], label: `Volume ${b.volume ?? ''}`.trim(), icon: HardDrive });
	}
	if (!out.length) out.push({ path: '/', label: 'Backup Root', icon: Layers });
	return out;
}

/** A snapshot node as a picker entry (devices, pipes and sockets are "other"). */
export function pickerEntry(n: BackupNode): PickerEntry {
	const type = n.type === 'dir' || n.type === 'file' || n.type === 'symlink' ? n.type : 'other';
	return { name: n.name, path: n.path, type, size: n.size, mtime: n.mtime };
}

/** Lists a backup's folders for the picker. */
export function backupSource(backupId: string, client: ApiClient = api): PickerSource {
	return {
		query: (dir) => ({
			queryKey: [...backupKeys.contents(backupId, dir), 'picker'],
			queryFn: async ({ signal }) => {
				const c = await unwrap(
					client.GET('/api/v1/backups/{backupId}/contents', {
						params: { path: { backupId }, query: { path: dir, limit: PICKER_LIMIT } },
						signal
					})
				);
				// Like restic, a listing starts with the folder itself.
				return {
					entries: c.entries.filter((e) => e.path !== dir).map(pickerEntry),
					truncated: c.truncated
				};
			}
		})
	};
}
