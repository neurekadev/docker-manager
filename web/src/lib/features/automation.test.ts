// Component tests of the automation and backup screens (#20, #14, #10):
// roles, labels, keyboard and the safety gates.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import type { Account } from '$lib/api/client';
import QueryHarness from '../../test/QueryHarness.svelte';
import StepUpDialog from '$lib/auth/StepUpDialog.svelte';
import { StepUpPrompt } from '$lib/auth/stepup.svelte';
import CoverageList from './common/CoverageList.svelte';
import RuleEditor from './maintenance/RuleEditor.svelte';
import CandidatesTable from './updates/CandidatesTable.svelte';
import RecoveryKeyChallenge from './backups/RecoveryKeyChallenge.svelte';
import ScopePreviewView from './backups/ScopePreviewView.svelte';
import type { MaintenanceRule } from './maintenance/model';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });
const newClient = () => new QueryClient({ defaultOptions: { queries: { retry: false } } });
const KEY = 'DYRK-4RN4-LUDA-QCP3-RQ2S-RV4J-OYB7-3PZF-CXUB-5YG2-MY7Z-O5A2-HZ4D-FGLA';

function json(body: unknown, status = 200) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' }
	});
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('RuleEditor (#14)', () => {
	it('keeps a volume rule off until its own data-loss opt-in is checked', async () => {
		const user = setup();
		let rule: MaintenanceRule = { category: 'named_volumes', enabled: false, minAgeHours: 720 };
		const onchange = vi.fn((r: MaintenanceRule) => (rule = r));
		const { rerender } = render(RuleEditor, { props: { rule, onchange } });
		const toggle = screen.getByRole('switch', { name: 'Named volumes' });
		expect(toggle).toBeDisabled();
		expect(screen.getByText('Deletes data')).toBeInTheDocument();
		await user.click(
			screen.getByRole('checkbox', {
				name: /^I understand that removing volumes deletes the data in them/
			})
		);
		expect(onchange).toHaveBeenLastCalledWith(
			expect.objectContaining({ volumeOptIn: true, enabled: false })
		);
		await rerender({ rule, onchange });
		expect(screen.getByRole('switch', { name: 'Named volumes' })).toBeEnabled();
		await user.click(screen.getByRole('switch', { name: 'Named volumes' }));
		expect(onchange).toHaveBeenLastCalledWith(
			expect.objectContaining({ enabled: true, volumeOptIn: true })
		);
	});

	it('withdrawing the opt-in turns the rule off again', async () => {
		const user = setup();
		const onchange = vi.fn();
		render(RuleEditor, {
			props: {
				rule: {
					category: 'anonymous_volumes',
					enabled: true,
					minAgeHours: 0,
					volumeOptIn: true
				},
				onchange
			}
		});
		await user.click(screen.getByRole('checkbox', { name: /deletes the data/ }));
		expect(onchange).toHaveBeenLastCalledWith(
			expect.objectContaining({ volumeOptIn: false, enabled: false })
		);
	});

	it('shows the options on request, with the age in days', async () => {
		const user = setup();
		render(RuleEditor, {
			props: {
				rule: { category: 'stopped_containers', enabled: true, minAgeHours: 720 },
				onchange: vi.fn()
			}
		});
		const more = screen.getByRole('button', { name: 'Options' });
		expect(more).toHaveAttribute('aria-expanded', 'false');
		await user.click(more);
		expect(screen.getByRole('button', { name: 'Hide options' })).toHaveAttribute(
			'aria-expanded',
			'true'
		);
		expect(screen.getByLabelText('Only remove objects older than')).toHaveValue(30);
		expect(screen.getByRole('group', { name: 'Container states' })).toBeInTheDocument();
	});
});

describe('CoverageList (#10, #20)', () => {
	const items = [
		{ key: 's1', label: 'silo' },
		{ key: 's2', label: 'shop', description: 'prod' },
		{ key: 's3', label: 'wiki' }
	];

	it('checks what the policy covers; unchecking adds the item to the exclusions', async () => {
		const user = setup();
		const onchange = vi.fn();
		render(CoverageList, {
			props: { label: 'Stacks covered', items, excluded: ['s3'], onchange }
		});
		const group = screen.getByRole('group', { name: 'Stacks covered' });
		expect(within(group).getByRole('checkbox', { name: /^silo/ })).toBeChecked();
		expect(within(group).getByRole('checkbox', { name: /^wiki/ })).not.toBeChecked();
		expect(group).toHaveTextContent('2 of 3 included');
		await user.click(within(group).getByRole('checkbox', { name: /^shop/ }));
		expect(onchange).toHaveBeenLastCalledWith(['s3', 's2']);
	});

	it('includes everything again on request', async () => {
		const user = setup();
		const onchange = vi.fn();
		const { rerender } = render(CoverageList, {
			props: { label: 'Stacks covered', items, excluded: ['s1', 'other'], onchange }
		});
		await user.click(screen.getByRole('button', { name: 'Include all' }));
		expect(onchange).toHaveBeenLastCalledWith(['other']);
		await rerender({ label: 'Stacks covered', items, excluded: [], onchange });
		expect(screen.queryByRole('button', { name: 'Include all' })).not.toBeInTheDocument();
	});
});

describe('CandidatesTable (#20)', () => {
	it('shows running and registry digests, the status and why a tag is risky', () => {
		render(CandidatesTable, {
			props: {
				label: 'Candidates',
				candidates: [
					{
						id: 'a',
						service: 'silo-web',
						reference: 'ghcr.io/silo/web:latest',
						eligible: true,
						nonVersionTag: true,
						status: 'update_available',
						currentDigest: 'sha256:1111111111111111aaaa',
						candidateDigest: 'sha256:2222222222222222bbbb'
					},
					{
						id: 'b',
						service: 'silo-db',
						reference: 'postgres@sha256:abc',
						eligible: false,
						nonVersionTag: false,
						status: 'ineligible',
						reason: 'digest_pinned'
					}
				]
			}
		});
		const table = screen.getByRole('table', { name: 'Candidates' });
		const web = within(table).getByRole('row', { name: /silo-web/ });
		expect(web).toHaveTextContent('Update available');
		expect(web).toHaveTextContent('Tag can change meaning');
		expect(web).toHaveTextContent('111111111111');
		expect(web).toHaveTextContent('222222222222');
		expect(within(table).getByRole('row', { name: /silo-db/ })).toHaveTextContent(
			'Pinned by @sha256 digest'
		);
	});
});

describe('RecoveryKeyChallenge (#10)', () => {
	it('needs a well-formed key and the saved statement, then confirms without keeping the key', async () => {
		const user = setup();
		const seen: unknown[] = [];
		vi.stubGlobal(
			'fetch',
			vi.fn(async (req: Request) => {
				expect(new URL(req.url).pathname).toBe(
					'/api/v1/backup-repositories/r1/recovery-confirmations'
				);
				seen.push(await req.json());
				return json({
					activated: true,
					keyState: { generation: 1, rotationInProgress: false, scope: 'instance' },
					repository: {
						id: 'r1',
						name: 'NAS',
						kind: 'local',
						state: 'ready',
						view: 'full',
						actions: []
					}
				});
			})
		);
		const onconfirmed = vi.fn();
		render(RecoveryKeyChallenge, {
			props: { repositoryId: 'r1', fingerprint: 'rk_0123', onconfirmed }
		});
		const submit = screen.getByRole('button', { name: 'Confirm Recovery Key' });
		expect(submit).toBeDisabled();
		expect(
			screen.getByText(/manager key store and your copy are both lost/)
		).toBeInTheDocument();
		await user.type(screen.getByLabelText('Recovery Key'), KEY.toLowerCase());
		expect(submit).toBeDisabled();
		await user.click(
			screen.getByRole('checkbox', {
				name: /^I saved the Recovery Key outside Docker Manager/
			})
		);
		expect(submit).toBeEnabled();
		await user.click(submit);
		await waitFor(() => expect(onconfirmed).toHaveBeenCalled());
		expect(seen).toEqual([{ recoveryKey: KEY, backedUp: true }]);
		expect(screen.getByLabelText('Recovery Key')).toHaveValue('');
	});

	it('explains a wrong key', async () => {
		const user = setup();
		vi.stubGlobal(
			'fetch',
			vi.fn(async () =>
				json(
					{
						code: 'recovery_key_mismatch',
						message: 'mismatch',
						requestId: 'r',
						retryable: false,
						details: []
					},
					422
				)
			)
		);
		render(RecoveryKeyChallenge, { props: { repositoryId: 'r1', onconfirmed: vi.fn() } });
		await user.type(screen.getByLabelText('Recovery Key'), KEY);
		await user.click(screen.getByRole('checkbox', { name: /I saved the Recovery Key/ }));
		await user.click(screen.getByRole('button', { name: 'Confirm Recovery Key' }));
		expect(await screen.findByRole('alert')).toHaveTextContent(
			'not this Docker Manager’s Recovery Key'
		);
	});
});

describe('ScopePreviewView (#10)', () => {
	const preview = {
		shutdown: true,
		environments: [
			{
				environmentId: 'e1',
				environmentName: 'homelab',
				repositoryId: 'r1',
				downtime: 'About 2 minutes for silo',
				items: [
					{
						item: 'silo',
						kind: 'stack',
						stackId: 's1',
						estimateComplete: true,
						estimatedBytes: 1024,
						estimatedFiles: 3,
						sources: [
							{ kind: 'project', path: '/stacks/silo', state: 'included' },
							{
								kind: 'external_path',
								path: '/srv/media',
								state: 'requires_opt_in',
								reason: 'outside the project'
							},
							{
								kind: 'anonymous_volume',
								path: '/v/anon',
								state: 'excluded',
								reason: 'anonymous volumes are off'
							}
						],
						affectedContainers: [
							{ name: 'silo-web', running: true, stopOrder: 1 },
							{ name: 'silo-db', running: true, stopOrder: 2 },
							{
								name: 'docker-agent',
								running: true,
								protected: 'Docker Manager itself'
							}
						]
					}
				]
			}
		]
	};

	it('lists every source with its state and opts external paths in explicitly', async () => {
		const user = setup();
		const onOptIn = vi.fn();
		render(ScopePreviewView, { props: { preview, onOptIn, optedIn: () => false } });
		expect(screen.getByText('/stacks/silo')).toBeInTheDocument();
		expect(screen.getByText('Needs opt-in')).toBeInTheDocument();
		expect(screen.getByText('Excluded')).toBeInTheDocument();
		await user.click(screen.getByRole('checkbox', { name: 'Include this path' }));
		expect(onOptIn).toHaveBeenCalledWith('s1', '/srv/media', true);
	});

	it('shows the shutdown order, the downtime and the containers that keep running', () => {
		render(ScopePreviewView, { props: { preview } });
		expect(screen.getByText('About 2 minutes for silo')).toBeInTheDocument();
		const table = screen.getByRole('table', { name: 'Containers stopped for silo' });
		const rows = within(table).getAllByRole('row').slice(1);
		expect(rows.map((r) => r.textContent)).toEqual([
			expect.stringContaining('silo-web'),
			expect.stringContaining('silo-db'),
			expect.stringContaining('Keeps running: Docker Manager itself')
		]);
	});
});

describe('StepUpDialog (#16)', () => {
	it('asks for the password (and TOTP) and settles the prompt after the step-up', async () => {
		const user = setup();
		const bodies: unknown[] = [];
		vi.stubGlobal(
			'fetch',
			vi.fn(async (req: Request) => {
				expect(new URL(req.url).pathname).toBe('/api/v1/auth/step-ups');
				bodies.push(await req.json());
				return json({
					state: 'authenticated',
					factors: [],
					missingFactors: [],
					requiredFactors: 'none'
				});
			})
		);
		const prompt = new StepUpPrompt();
		const user0: Account = {
			id: 'u1',
			username: 'admin',
			displayName: 'Admin',
			owner: true,
			status: 'active',
			groupId: 'g',
			revision: 1,
			createdAt: '',
			updatedAt: '',
			factors: { password: true, totp: true, passkeys: 0, recoveryCodesRemaining: 10 }
		};
		render(QueryHarness<ComponentProps<typeof StepUpDialog>>, {
			props: { client: newClient(), component: StepUpDialog, props: { user: user0, prompt } }
		});
		const done = prompt.request();
		const dialog = await screen.findByRole('dialog', { name: "Confirm it's you" });
		const confirm = within(dialog).getByRole('button', { name: 'Confirm with password' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText('Password'), 'correct horse');
		await user.type(within(dialog).getByLabelText('Authenticator code'), '123 456');
		await user.click(confirm);
		expect(await done).toBe(true);
		expect(bodies).toEqual([{ password: 'correct horse', totpCode: '123456' }]);
	});

	it('closing the dialog settles the prompt as cancelled', async () => {
		const user = setup();
		const prompt = new StepUpPrompt();
		render(QueryHarness<ComponentProps<typeof StepUpDialog>>, {
			props: {
				client: newClient(),
				component: StepUpDialog,
				props: {
					prompt,
					user: {
						id: 'u1',
						username: 'a',
						displayName: '',
						owner: false,
						status: 'active',
						groupId: 'g',
						revision: 1,
						createdAt: '',
						updatedAt: '',
						factors: {
							password: true,
							totp: false,
							passkeys: 0,
							recoveryCodesRemaining: 0
						}
					}
				}
			}
		});
		const done = prompt.request();
		await screen.findByRole('dialog', { name: "Confirm it's you" });
		await user.keyboard('{Escape}');
		expect(await done).toBe(false);
	});
});
