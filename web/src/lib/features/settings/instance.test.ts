// Component test of the instance settings card (#4 GET/PATCH /settings):
// the name is renamed in place, validated like the server, and the
// deployment configuration stays read-only.
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { ApiRequestError } from '$lib/api/client';
import InstanceCard from './InstanceCard.svelte';
import type { InstanceSettings } from './queries';

const settings: InstanceSettings = {
	name: 'DockYard',
	instanceId: '0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f',
	revision: 1,
	updatedAt: '2026-09-25T12:00:00Z',
	deployment: {
		publicUrl: 'https://docker.example.com',
		localDevelopment: false,
		trustedProxyCount: 1,
		streamHeartbeatSeconds: 15,
		filesMaxUploadBytes: 2 * 1024 ** 3,
		metricsEndpoint: false
	}
};

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

describe('InstanceCard (#4)', () => {
	it('shows the name, version and the read-only deployment settings', () => {
		render(InstanceCard, { props: { settings, version: 'edge (build abc)', onsave: vi.fn() } });
		expect(screen.getByRole('heading', { name: 'About this DockYard' })).toBeInTheDocument();
		expect(screen.getByText('DockYard')).toBeInTheDocument();
		expect(screen.getByText('https://docker.example.com')).toBeInTheDocument();
		expect(screen.getByText('1 address range')).toBeInTheDocument();
		expect(screen.getByText('2 GB')).toBeInTheDocument();
		expect(screen.getByText('edge (build abc)')).toBeInTheDocument();
		// Without settings.manage there is nothing to edit (hide, don't disable).
		expect(screen.queryByRole('button', { name: 'Rename' })).not.toBeInTheDocument();
	});

	it('renames with a trimmed, validated name', async () => {
		const user = setup();
		const onsave = vi.fn().mockResolvedValue(undefined);
		render(InstanceCard, { props: { settings, canEdit: true, onsave } });
		await user.click(screen.getByRole('button', { name: 'Rename' }));
		const field = screen.getByRole('textbox', { name: /Name/ });
		expect(field).toHaveValue('DockYard');
		await user.clear(field);
		await user.type(field, '   ');
		await user.click(screen.getByRole('button', { name: 'Rename DockYard' }));
		expect(onsave).not.toHaveBeenCalled();
		expect(screen.getByText('Enter a name.')).toBeInTheDocument();
		await user.clear(field);
		await user.type(field, '  Homelab {Enter}');
		expect(onsave).toHaveBeenCalledWith('Homelab');
		expect(screen.queryByRole('textbox', { name: /Name/ })).not.toBeInTheDocument();
	});

	it("shows the server's field error and keeps the form open", async () => {
		const user = setup();
		const onsave = vi.fn().mockRejectedValue(
			new ApiRequestError('invalid settings', 422, {
				code: 'validation_failed',
				message: 'invalid settings',
				details: [
					{
						field: 'body.name',
						message: 'the name must be 1-64 characters without control characters'
					}
				],
				requestId: 'r1',
				retryable: false
			})
		);
		render(InstanceCard, { props: { settings, canEdit: true, onsave } });
		await user.click(screen.getByRole('button', { name: 'Rename' }));
		await user.click(screen.getByRole('button', { name: 'Rename DockYard' }));
		expect(onsave).toHaveBeenCalledWith('DockYard');
		expect(
			await screen.findByText('the name must be 1-64 characters without control characters')
		).toBeInTheDocument();
		expect(screen.getByRole('textbox', { name: /Name/ })).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(screen.getByText('DockYard')).toBeInTheDocument();
	});
});
