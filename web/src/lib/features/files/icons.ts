// Entry icons of the file list (#15, the mockup's tree: blue folders, a
// braces icon for JSON). Decorative: the row's name and type are text.
import Braces from '@lucide/svelte/icons/braces';
import File from '@lucide/svelte/icons/file';
import FileArchive from '@lucide/svelte/icons/file-archive';
import FileCode from '@lucide/svelte/icons/file-code';
import FileImage from '@lucide/svelte/icons/file-image';
import FileLock from '@lucide/svelte/icons/file-lock';
import FileSymlink from '@lucide/svelte/icons/file-symlink';
import FileTerminal from '@lucide/svelte/icons/file-terminal';
import FileText from '@lucide/svelte/icons/file-text';
import Folder from '@lucide/svelte/icons/folder';
import FolderSymlink from '@lucide/svelte/icons/folder-symlink';
import type { IconComponent } from '$lib/design/icons';
import type { FileEntry } from './api';
import { extension } from './paths';

export type EntryTone = 'folder' | 'json' | 'code' | 'plain' | 'muted';

export interface EntryIcon {
	icon: IconComponent;
	tone: EntryTone;
}

export function entryIcon(e: Pick<FileEntry, 'name' | 'type' | 'linkStatus'>): EntryIcon {
	if (e.type === 'dir') return { icon: Folder, tone: 'folder' };
	if (e.type === 'symlink')
		return { icon: e.linkStatus === 'inside' ? FolderSymlink : FileSymlink, tone: 'muted' };
	if (e.type === 'other') return { icon: File, tone: 'muted' };
	const name = e.name.toLowerCase();
	if (name === '.env' || name.startsWith('.env.')) return { icon: FileLock, tone: 'plain' };
	switch (extension(name)) {
		case 'json':
			return { icon: Braces, tone: 'json' };
		case 'yaml':
		case 'yml':
		case 'toml':
		case 'xml':
		case 'conf':
		case 'ini':
			return { icon: FileCode, tone: 'plain' };
		case 'sh':
		case 'bash':
			return { icon: FileTerminal, tone: 'plain' };
		case 'md':
		case 'txt':
		case 'log':
			return { icon: FileText, tone: 'plain' };
		case 'zip':
		case 'gz':
		case 'tgz':
		case 'tar':
			return { icon: FileArchive, tone: 'plain' };
		case 'png':
		case 'jpg':
		case 'jpeg':
		case 'gif':
		case 'webp':
		case 'svg':
			return { icon: FileImage, tone: 'plain' };
	}
	return { icon: File, tone: 'plain' };
}

/** Human type of an entry for screen readers and the stacked layout. */
export function entryKind(e: Pick<FileEntry, 'type' | 'linkStatus'>): string {
	switch (e.type) {
		case 'dir':
			return 'Folder';
		case 'symlink':
			return e.linkStatus === 'outside'
				? 'Link outside this root'
				: e.linkStatus === 'dangling'
					? 'Broken link'
					: 'Link';
		case 'other':
			return 'Special file';
		default:
			return 'File';
	}
}

/** The owner in words: "root" for ID 0, else the numeric IDs ("1000:1000", "root:999"). */
export function ownerText(uid: number, gid: number): string {
	const id = (n: number) => (n === 0 ? 'root' : String(n));
	return uid === gid ? id(uid) : `${id(uid)}:${id(gid)}`;
}

/** The owner's tooltip: "User ID 1000, group ID 1000". */
export function ownerTitle(uid: number, gid: number): string {
	return `User ID ${uid}, group ID ${gid}`;
}

/** "rw-r--r--" from an octal mode string ("0644"). */
export function modeString(mode: string): string {
	const bits = parseInt(mode.slice(-3), 8);
	if (Number.isNaN(bits)) return mode;
	const rwx = (n: number) => `${n & 4 ? 'r' : '-'}${n & 2 ? 'w' : '-'}${n & 1 ? 'x' : '-'}`;
	return rwx((bits >> 6) & 7) + rwx((bits >> 3) & 7) + rwx(bits & 7);
}
