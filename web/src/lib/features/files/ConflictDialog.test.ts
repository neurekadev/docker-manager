// Conflict prompts (#15): per item, "Apply to All" off by default, cancel
// aborts the operation.
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import ConflictDialog from './ConflictDialog.svelte';
import type { ConflictChoice, ConflictItem } from './conflicts';

const existing = { name: 'x', type: 'file', size: 2048, modifiedAt: '2026-09-25T10:00:00Z' };
const items: ConflictItem[] = [
	{ source: 'a.yaml', destination: 'config/a.yaml', existing },
	{ source: 'b.yaml', destination: 'config/b.yaml', existing },
	{ source: 'c.yaml', destination: 'c.yaml', existing }
];

function setup(props: Record<string, unknown> = {}) {
	const results: (Map<string, ConflictChoice> | null)[] = [];
	const user = userEvent.setup({ pointerEventsCheck: 0 });
	render(ConflictDialog, {
		props: {
			open: true,
			title: 'Copy 3 items to config',
			items,
			rootLabel: 'silo',
			onresolve: (d: Map<string, ConflictChoice> | null) => results.push(d),
			...props
		}
	});
	return { user, results };
}

describe('ConflictDialog', () => {
	it('asks per item; apply to all is off until chosen', async () => {
		const { user, results } = setup();
		expect(
			screen.getByRole('alertdialog', { name: 'Copy 3 items to config' })
		).toBeInTheDocument();
		expect(screen.getByText('Conflict 1 of 3')).toBeInTheDocument();
		const all = screen.getByRole('checkbox', { name: 'Apply to All 3 Remaining Conflicts' });
		expect(all).not.toBeChecked();
		await user.click(screen.getByRole('button', { name: 'Replace' }));
		expect(screen.getByText('Conflict 2 of 3')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Keep Both (b (1).yaml)' }));
		expect(screen.getByText('Conflict 3 of 3')).toBeInTheDocument();
		// A root-level destination names the root.
		expect(screen.getByText('silo')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Skip' }));
		expect(results).toHaveLength(1);
		expect([...results[0]!]).toEqual([
			['a.yaml', 'overwrite'],
			['b.yaml', 'keep_both'],
			['c.yaml', 'skip']
		]);
	});

	it('applies one decision to the rest when asked, and cancel aborts', async () => {
		const { user, results } = setup();
		await user.click(
			screen.getByRole('checkbox', { name: 'Apply to All 3 Remaining Conflicts' })
		);
		await user.click(screen.getByRole('button', { name: 'Skip' }));
		expect([...results[0]!.values()]).toEqual(['skip', 'skip', 'skip']);

		const second = setup();
		await second.user.click(screen.getAllByRole('button', { name: 'Cancel' }).at(-1)!);
		expect(second.results).toEqual([null]);
	});

	it('offers only keep both or skip when pasting into the same folder', async () => {
		const { user, results } = setup({
			items: [{ source: 'a.yaml', destination: 'a.yaml', existing, self: true }],
			title: 'Copy a.yaml to silo'
		});
		expect(screen.getByText(/pasted into its own folder/)).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Replace' })).toBeNull();
		await user.click(screen.getByRole('button', { name: /^Keep Both/ }));
		expect([...results[0]!]).toEqual([['a.yaml', 'keep_both']]);
	});

	it('asks once for a whole extraction', async () => {
		const { user, results } = setup({ single: true, title: 'Extract backup.zip' });
		expect(screen.getByText('3 entries already exist at the destination.')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Replace All' }));
		expect([...results[0]!.values()]).toEqual(['overwrite', 'overwrite', 'overwrite']);
	});
});
