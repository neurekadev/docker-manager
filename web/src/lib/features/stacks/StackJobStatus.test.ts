import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { routes } from '$lib/routes';
import StackJobStatus from './StackJobStatus.svelte';

describe('StackJobStatus', () => {
	it('says what runs and links to the job', () => {
		render(StackJobStatus, {
			props: { job: { id: 'job-7', kind: 'stack.deploy', state: 'running' } }
		});
		const link = screen.getByRole('link', { name: 'Deploying' });
		expect(link).toHaveAttribute('href', routes.job('job-7'));
		expect(link).toHaveAttribute('title', 'Deploy Stack: open the job');
		expect(link).toHaveAttribute('aria-busy', 'true');
	});
});
