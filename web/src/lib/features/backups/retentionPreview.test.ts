// Retention preview (#10): opened once, it follows unsaved rule changes
// after a short pause, shows only the newest answer, and names a
// snapshot removed because its stack or volume was deleted.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import RetentionPreviewPanel from './RetentionPreviewPanel.svelte';

function json(body: unknown) {
	return new Response(JSON.stringify(body), {
		status: 200,
		headers: { 'Content-Type': 'application/json' }
	});
}

/** Stubs the preview endpoint; returns the retention bodies it received. */
function stubPreview() {
	const bodies: unknown[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request) => {
			const body = (await input.json().catch(() => ({}))) as {
				retention?: { last?: number };
			};
			bodies.push(body.retention);
			const last = body.retention?.last ?? 0;
			return json({
				retention: body.retention ?? {},
				locations: [
					{
						repositoryId: 'r1',
						scope: 'env:e1',
						keep: 1,
						forget: 1,
						decisions: [
							{
								snapshotId: 'a',
								time: '2026-09-02T00:00:00Z',
								item: 'volume/media',
								keep: true,
								reasons: [`last${last}`]
							},
							{
								snapshotId: 'b',
								time: '2026-09-01T00:00:00Z',
								item: 'volume/gone',
								keep: false,
								reasons: ['deleted']
							}
						]
					}
				]
			});
		})
	);
	return bodies;
}

afterEach(() => vi.unstubAllGlobals());

describe('RetentionPreviewPanel', () => {
	it('refreshes an opened preview when the rules change', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const bodies = stubPreview();
		const { rerender } = render(RetentionPreviewPanel, {
			props: { policyId: 'p1', retention: { last: 5 } }
		});
		// Nothing is asked before the preview is opened.
		await rerender({ policyId: 'p1', retention: { last: 6 } });
		await new Promise((r) => setTimeout(r, 500));
		expect(bodies).toEqual([]);

		await user.click(screen.getByRole('button', { name: 'Preview Retention' }));
		expect(await screen.findByText('kept by last6')).toBeInTheDocument();
		expect(screen.getByText('its stack or volume was deleted')).toBeInTheDocument();
		expect(screen.queryByText(/kept by deleted/)).toBeNull();

		await rerender({ policyId: 'p1', retention: { last: 7 } });
		expect(await screen.findByText('kept by last7', {}, { timeout: 2000 })).toBeInTheDocument();
		expect(bodies).toEqual([{ last: 6 }, { last: 7 }]);
	});

	it('names repositories, environments and items and reports what it would remove', async () => {
		stubPreview();
		const states: { ready: boolean; forget: number }[] = [];
		render(RetentionPreviewPanel, {
			props: {
				policyId: 'p1',
				auto: true,
				environmentName: (id: string) => (id === 'e1' ? 'Hyperion' : id),
				repositoryName: (id: string) => (id === 'r1' ? 'Offsite' : undefined),
				onstate: (s: { ready: boolean; forget: number }) => states.push(s)
			}
		});
		expect(await screen.findByText('Offsite')).toBeInTheDocument();
		expect(screen.getByText('Environment Hyperion')).toBeInTheDocument();
		expect(screen.getByText('Volume media')).toBeInTheDocument();
		expect(screen.getByText('Volume gone')).toBeInTheDocument();
		expect(screen.getByText(/^Removes/)).toHaveTextContent('Removes 1 backup, keeps 1.');
		expect(screen.queryByText('env:e1')).toBeNull();
		expect(states.at(-1)).toEqual({ ready: true, forget: 1 });
	});
});
