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
		const toggle = screen.getByRole('switch', { name: 'Named Volumes' });
		expect(toggle).toBeDisabled();
		expect(screen.getByText('Deletes Data')).toBeInTheDocument();
		await user.click(
			screen.getByRole('checkbox', {
				name: /^I Understand That Removing Volumes Deletes the Data in Them/
			})
		);
		expect(onchange).toHaveBeenLastCalledWith(
			expect.objectContaining({ volumeOptIn: true, enabled: false })
		);
		await rerender({ rule, onchange });
		expect(screen.getByRole('switch', { name: 'Named Volumes' })).toBeEnabled();
		await user.click(screen.getByRole('switch', { name: 'Named Volumes' }));
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
		await user.click(screen.getByRole('checkbox', { name: /Deletes the Data/ }));
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
		expect(screen.getByRole('button', { name: 'Hide Options' })).toHaveAttribute(
			'aria-expanded',
			'true'
		);
		expect(screen.getByLabelText('Only Remove Objects Older Than')).toHaveValue(30);
		expect(screen.getByRole('group', { name: 'Container States' })).toBeInTheDocument();
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
		await user.click(screen.getByRole('button', { name: 'Include All' }));
		expect(onchange).toHaveBeenLastCalledWith(['other']);
		await rerender({ label: 'Stacks covered', items, excluded: [], onchange });
		expect(screen.queryByRole('button', { name: 'Include All' })).not.toBeInTheDocument();
	});

	it('shows a locked item unchecked and disabled, with its reason, never included', async () => {
		const user = setup();
		const onchange = vi.fn();
		const reason =
			'Managed by a volume label: docker-manager.backup.exclude=true leaves it out of backups.';
		const locked = [...items, { key: 'cache', label: 'cache', locked: reason }];
		const { rerender } = render(CoverageList, {
			props: { label: 'Volumes covered', items: locked, excluded: [], onchange }
		});
		const group = screen.getByRole('group', { name: 'Volumes covered' });
		const box = within(group).getByRole('checkbox', { name: /^cache/ });
		expect(box).not.toBeChecked();
		expect(box).toBeDisabled();
		expect(within(group).getByRole('img', { name: reason })).toBeInTheDocument();
		expect(group).toHaveTextContent('3 of 4 included');
		// Nothing the user left out: no Include All for the locked item.
		expect(screen.queryByRole('button', { name: 'Include All' })).not.toBeInTheDocument();
		await rerender({
			label: 'Volumes covered',
			items: locked,
			excluded: ['s1', 'cache'],
			onchange
		});
		await user.click(screen.getByRole('button', { name: 'Include All' }));
		expect(onchange).toHaveBeenLastCalledWith(['cache']);
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
						candidateDigest: 'sha256:2222222222222222bbbb',
						publishedAt: new Date(Date.now() - 3 * 86_400_000).toISOString()
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
		expect(web).toHaveTextContent('Update Available');
		expect(web).toHaveTextContent('Tag Can Change Meaning');
		expect(web).toHaveTextContent('111111111111');
		expect(web).toHaveTextContent('222222222222');
		expect(web).toHaveTextContent('published 3 days ago');
		const db = within(table).getByRole('row', { name: /silo-db/ });
		expect(db).toHaveTextContent('Pinned by @sha256 digest');
		expect(db).not.toHaveTextContent('published');
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
				name: /^I Saved the Recovery Key Outside Docker Manager/
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
		await user.click(screen.getByRole('checkbox', { name: /I Saved the Recovery Key/ }));
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
		expect(screen.getByText('Needs Opt-In')).toBeInTheDocument();
		expect(screen.getByText('Excluded')).toBeInTheDocument();
		await user.click(screen.getByRole('checkbox', { name: 'Include This Path' }));
		expect(onOptIn).toHaveBeenCalledWith('s1', '/srv/media', true);
	});

	it('shows the shutdown order, the downtime and the containers that keep running', () => {
		render(ScopePreviewView, { props: { preview } });
		expect(screen.getByText('About 2 minutes for silo')).toBeInTheDocument();
		const table = screen.getByRole('table', { name: 'Containers Stopped for silo' });
		const rows = within(table).getAllByRole('row').slice(1);
		expect(rows.map((r) => r.textContent)).toEqual([
			expect.stringContaining('silo-web'),
			expect.stringContaining('silo-db'),
			expect.stringContaining('Keeps running: Docker Manager itself')
		]);
	});

	it('renders repeated sources once and names stacks, never by ID', () => {
		const vol = (name: string) => ({
			kind: 'volume',
			name,
			path: '',
			state: 'excluded',
			reason: 'the volume does not exist (not created yet)'
		});
		const bind = (service: string) => ({
			kind: 'external_path',
			path: '/etc/localtime',
			service,
			state: 'requires_opt_in',
			reason: 'outside the project'
		});
		const repeated = {
			shutdown: false,
			environments: [
				{
					environmentId: 'e1',
					environmentName: 'hyperion',
					repositoryId: 'r1',
					items: [
						{
							item: 'stack/0190-aaaa',
							kind: 'stack',
							stackId: '0190-aaaa',
							estimateComplete: false,
							estimatedBytes: 2048,
							estimatedFiles: 1200,
							sources: [
								vol('a'),
								vol('b'),
								vol('a'),
								bind('web'),
								bind('web'),
								bind('api')
							],
							conflicts: ['shared volume', 'shared volume'],
							warnings: ['slow', 'slow']
						}
					]
				}
			]
		};
		render(ScopePreviewView, {
			props: {
				preview: repeated,
				stackName: (id: string) => (id === '0190-aaaa' ? 'media' : undefined)
			}
		});
		expect(screen.getByText('media')).toBeInTheDocument();
		expect(screen.queryByText(/0190-aaaa/)).toBeNull();
		// a and b once each; the bind once per service.
		expect(screen.getAllByText('/etc/localtime')).toHaveLength(2);
		expect(screen.getByText('2 excluded')).toBeInTheDocument();
		expect(screen.getByText('2 needs opt-in')).toBeInTheDocument();
		expect(screen.getAllByText('shared volume')).toHaveLength(2);
		// Something needs the user: the details are open.
		expect(screen.getByText('media').closest('details')).toHaveAttribute('open');
	});
});

describe('StepUpDialog (#16, #186)', () => {
	const account = (factors: Account['factors']): Account => ({
		id: 'u1',
		username: 'admin',
		displayName: 'Admin',
		owner: true,
		status: 'active',
		groupIds: [],
		revision: 1,
		createdAt: '',
		updatedAt: '',
		factors
	});
	const session = () =>
		json({ state: 'authenticated', factors: [], missingFactors: [], requiredFactors: 'none' });

	/** Records the step-up bodies; passkey options answer a fixed challenge. */
	function stubApi() {
		const bodies: unknown[] = [];
		vi.stubGlobal(
			'fetch',
			vi.fn(async (req: Request) => {
				const path = new URL(req.url).pathname;
				if (path === '/api/v1/auth/passkeys/authentication-options')
					return json({ publicKey: { challenge: 'AAAA', allowCredentials: [] } });
				expect(path).toBe('/api/v1/auth/step-ups');
				bodies.push(await req.json());
				return session();
			})
		);
		return bodies;
	}

	/** A memory localStorage (the test environment may not provide one). */
	function stubStorage() {
		const data = new Map<string, string>();
		const storage = {
			getItem: (k: string) => data.get(k) ?? null,
			setItem: (k: string, v: string) => void data.set(k, v),
			removeItem: (k: string) => void data.delete(k)
		};
		vi.stubGlobal('localStorage', storage);
		return storage;
	}

	function open(user: Account) {
		const prompt = new StepUpPrompt();
		render(QueryHarness<ComponentProps<typeof StepUpDialog>>, {
			props: { client: newClient(), component: StepUpDialog, props: { user, prompt } }
		});
		return prompt.request();
	}

	it('asks an account with an authenticator app for the code alone', async () => {
		const user = setup();
		const bodies = stubApi();
		const done = open(
			account({ password: true, totp: true, passkeys: 0, recoveryCodesRemaining: 10 })
		);
		const dialog = await screen.findByRole('dialog', { name: "Confirm It's You" });
		expect(within(dialog).queryByLabelText('Password')).toBeNull();
		const confirm = within(dialog).getByRole('button', { name: 'Confirm' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText('Authenticator Code'), '123 456');
		await user.click(confirm);
		expect(await done).toBe(true);
		expect(bodies).toEqual([{ totpCode: '123456' }]);
	});

	it('falls back to the password on request', async () => {
		const user = setup();
		const bodies = stubApi();
		const storage = stubStorage();
		const done = open(
			account({ password: true, totp: true, passkeys: 0, recoveryCodesRemaining: 10 })
		);
		const dialog = await screen.findByRole('dialog', { name: "Confirm It's You" });
		await user.click(within(dialog).getByRole('button', { name: 'Use Your Password Instead' }));
		expect(within(dialog).queryByLabelText('Authenticator Code')).toBeNull();
		await user.type(within(dialog).getByLabelText('Password'), 'correct horse');
		await user.click(within(dialog).getByRole('button', { name: 'Confirm' }));
		expect(await done).toBe(true);
		expect(bodies).toEqual([{ password: 'correct horse' }]);
		// The fallback is not remembered: the code stays the default.
		expect(storage.getItem('docker-manager:verify-with')).toBeNull();
	});

	it('asks an account without a second factor for the password', async () => {
		const user = setup();
		const bodies = stubApi();
		const done = open(
			account({ password: true, totp: false, passkeys: 0, recoveryCodesRemaining: 0 })
		);
		const dialog = await screen.findByRole('dialog', { name: "Confirm It's You" });
		expect(within(dialog).queryByLabelText('Authenticator Code')).toBeNull();
		await user.type(within(dialog).getByLabelText('Password'), 'correct horse');
		await user.click(within(dialog).getByRole('button', { name: 'Confirm' }));
		expect(await done).toBe(true);
		expect(bodies).toEqual([{ password: 'correct horse' }]);
	});

	it('starts the passkey at once and can switch to the code, remembering it', async () => {
		const user = setup();
		const bodies = stubApi();
		const storage = stubStorage();
		vi.stubGlobal('isSecureContext', true);
		vi.stubGlobal('PublicKeyCredential', class {});
		// The browser's prompt stays open until the switch aborts it.
		const get = vi.fn(
			(o: CredentialRequestOptions) =>
				new Promise((_, reject) =>
					o.signal?.addEventListener('abort', () =>
						reject(new DOMException('aborted', 'AbortError'))
					)
				)
		);
		Object.defineProperty(navigator, 'credentials', { configurable: true, value: { get } });
		try {
			const done = open(
				account({ password: true, totp: true, passkeys: 1, recoveryCodesRemaining: 10 })
			);
			const dialog = await screen.findByRole('dialog', { name: "Confirm It's You" });
			await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
			expect(within(dialog).queryByLabelText('Password')).toBeNull();
			expect(
				within(dialog).getByRole('button', { name: 'Use Your Password Instead' })
			).toBeInTheDocument();
			await user.click(
				within(dialog).getByRole('button', { name: 'Use Authenticator Code Instead' })
			);
			expect(storage.getItem('docker-manager:verify-with')).toBe('totp');
			await user.type(within(dialog).getByLabelText('Authenticator Code'), '654321');
			await user.click(within(dialog).getByRole('button', { name: 'Confirm' }));
			expect(await done).toBe(true);
			expect(bodies).toEqual([{ totpCode: '654321' }]);
		} finally {
			Reflect.deleteProperty(navigator, 'credentials');
		}
	});

	it('closing the dialog settles the prompt as cancelled', async () => {
		const user = setup();
		const done = open(
			account({ password: true, totp: false, passkeys: 0, recoveryCodesRemaining: 0 })
		);
		await screen.findByRole('dialog', { name: "Confirm It's You" });
		await user.keyboard('{Escape}');
		expect(await done).toBe(false);
	});
});
