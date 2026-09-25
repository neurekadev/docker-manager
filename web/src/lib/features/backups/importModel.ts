// Fresh-manager import (#24): presentation of the connection test and the
// backup sets found in the portable manifests. Pure (import.spec.ts).
import type { Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui/Badge.svelte';
import type { Destination } from './destination';
import { normalizeRecoveryKey } from './model';

export type ImportTest = Schema<'BackupImportConnectionTest'>;
export type ImportPreview = Schema<'BackupImportPreview'>;
export type ImportSet = Schema<'BackupImportSet'>;
export type ImportMember = Schema<'BackupImportMember'>;
export type ImportLocation = Schema<'BackupImportLocation'>;
export type ImportSource = Schema<'BackupImportSource'>;

/** The request body shared by test, preview and import. */
export function importSource(
	d: Destination,
	recoveryKey: string,
	previousKey: string,
	extra: Partial<ImportSource> = {}
): ImportSource {
	const base: ImportSource =
		d.kind === 'local'
			? { kind: 'local', path: d.path.trim(), recoveryKey: normalizeRecoveryKey(recoveryKey) }
			: {
					kind: 's3',
					endpoint: d.endpoint.trim(),
					bucket: d.bucket.trim(),
					prefix: d.prefix.trim() || undefined,
					region: d.region.trim() || undefined,
					pathStyle: d.pathStyle,
					accessKeyId: d.accessKeyId.trim(),
					secretAccessKey: d.secretAccessKey,
					recoveryKey: normalizeRecoveryKey(recoveryKey)
				};
	if (previousKey.trim()) base.previousRecoveryKey = normalizeRecoveryKey(previousKey);
	return { ...base, ...extra };
}

const LOCATED: Record<ImportMember['located'], { tone: BadgeTone; label: string }> = {
	found: { tone: 'ok', label: 'Found' },
	missing: { tone: 'danger', label: 'Missing' },
	unverified: { tone: 'warn', label: 'Not reachable yet' },
	not_backed_up: { tone: 'neutral', label: 'Not backed up' }
};

export function located(m: ImportMember['located']) {
	return LOCATED[m] ?? { tone: 'neutral' as BadgeTone, label: m };
}

/** Whether a location can be read now, and with which key. */
export function locationState(l: ImportLocation): { tone: BadgeTone; label: string } {
	if (!l.found) return { tone: 'neutral', label: 'Not found' };
	if (!l.reachable) return { tone: 'warn', label: 'Not reachable from here' };
	if (l.key === 'previous') return { tone: 'warn', label: 'Opens with the previous key' };
	if (l.key === 'current') return { tone: 'ok', label: 'Opens with the Recovery Key' };
	return {
		tone: 'danger',
		label: l.errorClass ? l.errorClass.replaceAll('_', ' ') : 'Cannot open'
	};
}

const BUNDLE: Record<NonNullable<ImportSet['keyBundle']>, string> = {
	ok: 'The Recovery Key opens this set’s encrypted settings.',
	previous_key: 'This set’s settings open only with the previous Recovery Key: enter it too.',
	mismatch: 'The Recovery Key does not open this set’s encrypted settings.',
	corrupt: 'This set’s encrypted settings are damaged: choose another set.',
	missing: 'This set has no encrypted settings to restore: choose another set.'
};

export function bundleText(b: ImportSet['keyBundle']): string | null {
	return b ? BUNDLE[b] : null;
}

/** Why a set cannot be imported (null when it can). */
export function importBlocker(s: ImportSet): string | null {
	if (s.hostOnly) return 'Known only from a host repository: the manager state is not in it.';
	if (!s.schemaCompatible)
		return `Written by a newer DockYard${s.appVersion ? ` (${s.appVersion})` : ''}: install at least that version.`;
	if (!s.importable) return s.problems[0] ?? 'This set cannot be imported.';
	return null;
}

/** Error copy of the import routes (#24 error table). */
export const IMPORT_ERRORS: Record<string, string> = {
	backup_import_key_rejected:
		'The Recovery Key opens neither the manager repository nor a host repository. Check it for typos; after a rotation also enter the previous key. A lost Recovery Key cannot be recovered by anyone.',
	backup_import_not_found:
		'No DockYard repository is at this destination. Check the endpoint, bucket and prefix, or that the directory is mounted below DOCKYARD_BACKUP_LOCAL_ROOTS.',
	backup_import_manifest_corrupt:
		'The manifest of this backup set is damaged. Choose another set.',
	backup_import_schema_incompatible:
		'A newer DockYard wrote this set. Install at least that version, then import again.',
	backup_import_key_rotated:
		'This set is sealed under another Recovery Key (it was rotated). Enter the newest key and the previous one.',
	backup_import_state_missing:
		'This set has no readable manager state. Choose another set, or recover hosts directly with restic.',
	backup_import_unreachable: 'The storage could not be read.',
	backup_import_in_progress: 'An import is already running. Wait for it to finish.',
	setup_complete:
		'This DockYard already has an owner: import works only on a new, empty DockYard.',
	insecure_origin: 'Open this page on DockYard’s public HTTPS address to import.'
};
