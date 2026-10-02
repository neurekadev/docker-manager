// chmod/chown dialog (#15): mode checkboxes and octal stay in step, folders
// can get their own mode, recursion shows the previewed impact, owner IDs
// are validated, and the request carries exactly what was chosen.
import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import type { FileEntry, FilePreview } from './api';
import PermissionsDialog, { type PermissionChange } from './PermissionsDialog.svelte';

const dir: FileEntry = {
	name: 'config',
	path: 'config',
	type: 'dir',
	size: 0,
	mode: '0755',
	uid: 1000,
	gid: 100,
	modifiedAt: '2026-09-25T10:00:00Z'
};

function setup(props: Record<string, unknown> = {}) {
	const submitted: PermissionChange[] = [];
	const previews: boolean[] = [];
	const user = userEvent.setup({ pointerEventsCheck: 0 });
	render(PermissionsDialog, {
		props: {
			open: true,
			entries: [dir],
			canChmod: true,
			canChown: true,
			preview: async (recursive: boolean): Promise<FilePreview> => {
				previews.push(recursive);
				return {
					conflicts: [],
					conflictsTruncated: false,
					impact: {
						entries: recursive ? 5 : 1,
						files: recursive ? 3 : 0,
						dirs: recursive ? 2 : 1,
						symlinks: recursive ? 0 : 0,
						other: 0,
						bytes: 2048,
						truncated: false
					}
				};
			},
			onsubmit: async (c: PermissionChange) => void submitted.push(c),
			...props
		}
	});
	return { user, submitted, previews };
}

describe('PermissionsDialog', () => {
	it('keeps checkboxes and octal in step and previews the recursive impact', async () => {
		const { user, submitted, previews } = setup();
		const octal = screen.getByLabelText(/^Mode/);
		expect(octal).toHaveValue('0755');
		await user.click(screen.getByRole('checkbox', { name: 'Group Write' }));
		expect(octal).toHaveValue('0775');
		await user.clear(octal);
		await user.type(octal, '0640');
		expect(screen.getByRole('checkbox', { name: 'Owner Execute' })).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Group Read' })).toBeChecked();
		await waitFor(() => expect(screen.getByText(/Changes 1 folder/)).toBeInTheDocument());
		await user.click(
			screen.getByRole('switch', { name: 'Apply to Everything Inside the Selected Folders' })
		);
		await waitFor(() =>
			expect(screen.getByText(/Changes 2 folders, 3 files \(2 KB\)/)).toBeInTheDocument()
		);
		expect(previews).toEqual([false, true]);
		await user.click(screen.getByRole('switch', { name: 'Use Another Mode for Folders' }));
		expect(screen.getByLabelText('Mode for Folders')).toHaveValue('0755');
		await user.click(screen.getByRole('button', { name: 'Change Permissions' }));
		expect(submitted).toEqual([
			{ recursive: true, chmod: { mode: '0640', dirMode: '0755' }, chown: undefined }
		]);
	});

	it('changes owners with validated numeric IDs', async () => {
		const { user, submitted } = setup();
		await user.click(screen.getByRole('switch', { name: 'Change Mode' }));
		await user.click(screen.getByRole('switch', { name: 'Change Owner' }));
		expect(screen.getByLabelText('Owner ID (UID)')).toHaveValue('1000');
		expect(screen.getByLabelText('Group ID (GID)')).toHaveValue('100');
		await user.clear(screen.getByLabelText('Owner ID (UID)'));
		await user.type(screen.getByLabelText('Owner ID (UID)'), 'root');
		expect(screen.getByText('Use numeric IDs from 0 to 2147483647.')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Change Permissions' })).toBeDisabled();
		await user.clear(screen.getByLabelText('Owner ID (UID)'));
		await user.type(screen.getByLabelText('Owner ID (UID)'), '0');
		await user.click(screen.getByRole('button', { name: 'Change Permissions' }));
		expect(submitted).toEqual([
			{ recursive: false, chmod: undefined, chown: { uid: 0, gid: 100 } }
		]);
	});

	it('offers only what the capabilities allow', () => {
		setup({ canChmod: false });
		expect(screen.queryByRole('switch', { name: 'Change Mode' })).toBeNull();
		expect(screen.getByRole('switch', { name: 'Change Owner' })).toBeChecked();
	});
});
