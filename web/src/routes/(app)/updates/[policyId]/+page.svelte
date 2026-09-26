<script lang="ts">
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, unwrap, unwrapEmpty, type Schema } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, Card, Notice, PageHeader, toast } from '$lib/ui';
	import {
		environmentName,
		ifMatch,
		newIdempotencyKey,
		stacksQuery
	} from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import {
		environmentUpdateKeys,
		environmentUpdatePolicyQuery
	} from '$lib/features/updates/queries';

	type Preview = Schema<'EnvironmentPreviewOutputBody'>;
	const id = $derived(page.params.policyId ?? '');
	const qc = useQueryClient();
	const policy = createQuery(() => environmentUpdatePolicyQuery(id));
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());
	const targets = createQuery(() => ({
		queryKey: environmentUpdateKeys.targets(id),
		queryFn: async ({ signal }: { signal: AbortSignal }) =>
			(
				await unwrap(
					api.GET('/api/v1/environment-update-policies/{policyId}/targets', {
						params: { path: { policyId: id } },
						signal
					})
				)
			).items
	}));
	let preview = $state<Preview | null>(null);
	let error = $state<unknown>(null);
	let busy = $state(false);
	let confirmDelete = $state(false);
	usePage(() => ({
		title: policy.data?.name ?? 'Update policy',
		crumbs: [
			{ label: 'Updates', href: routes.updates() },
			{ label: policy.data?.name ?? 'Update policy' }
		]
	}));
	function targetName(type: string, id: string): string {
		if (type === 'container') return id;
		const stack = stacks.data?.find((s) => s.id === id);
		return stack?.displayName || stack?.name || id;
	}
	async function check() {
		busy = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/environment-update-policies/{policyId}/checks', {
					params: {
						path: { policyId: id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					}
				})
			);
			toast.success(`Checking ${out.jobs.length} targets`);
			await qc.invalidateQueries({ queryKey: ['policies'] });
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
	async function loadPreview() {
		busy = true;
		error = null;
		try {
			preview = await unwrap(
				api.POST('/api/v1/environment-update-policies/{policyId}/previews', {
					params: { path: { policyId: id } }
				})
			);
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
	async function run() {
		if (!preview) return;
		busy = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/environment-update-policies/{policyId}/runs', {
					params: {
						path: { policyId: id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { fingerprint: preview.fingerprint }
				})
			);
			preview = null;
			toast.success(`Started ${out.jobs.length} update jobs`);
			await qc.invalidateQueries({ queryKey: ['policies'] });
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
	async function remove() {
		const p = policy.data;
		if (!p) return;
		busy = true;
		error = null;
		try {
			await unwrapEmpty(
				api.DELETE('/api/v1/environment-update-policies/{policyId}', {
					params: { path: { policyId: id }, header: { 'If-Match': ifMatch(p.revision) } }
				})
			);
			await qc.invalidateQueries({ queryKey: ['policies'] });
			toast.success('Update policy deleted');
			await goto(routes.updates());
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
</script>

<Page>
	<QueryView query={policy} errorTitle="The update policy could not be loaded.">
		{#snippet children(p)}
			<PageHeader
				title={p.name}
				description={p.scope === 'all'
					? 'All Environments'
					: environmentName(envs.data, p.environmentId)}
			>
				{#snippet actions()}<Button href={routes.updatePolicyEdit(id)}>Edit policy</Button
					>{/snippet}
			</PageHeader>
			{#if error}<Notice tone="danger" title="Action failed">{actionError(error)}</Notice
				>{/if}
			<Card title="Schedule">
				<p>
					Checks: <ScheduleSummary
						compact
						cron={p.checkSchedule.cron ?? ''}
						timeZone={p.checkSchedule.timeZone ?? ''}
						enabled={p.checkSchedule.enabled}
					/>
				</p>
				<p>
					Automatic updates: <ScheduleSummary
						compact
						cron={p.runSchedule.cron ?? ''}
						timeZone={p.runSchedule.timeZone ?? ''}
						enabled={p.runSchedule.enabled}
					/>
				</p>
			</Card>
			<Card
				title="Targets"
				subtitle="Managed stacks and standalone containers are included unless excluded."
			>
				{#if targets.isPending}<p>Loading targets…</p>
				{:else if targets.isError}<p>Targets could not be loaded.</p>
				{:else if !targets.data?.length}<p>
						No eligible managed stacks or containers are in this scope.
					</p>
				{:else}
					<ul>
						{#each targets.data as target (target.policyId)}
							<li>
								{environmentName(envs.data, target.environmentId)} / {targetName(
									target.type,
									target.id
								)} — {target.inactive
									? 'Excluded'
									: `${target.candidateSummary.available} updates available, ${target.candidateSummary.upToDate} up to date`}
							</li>
						{/each}
					</ul>
				{/if}
				<div class="actions">
					<Button onclick={check} disabled={busy}>Check updates</Button><Button
						onclick={loadPreview}
						disabled={busy}>Preview updates</Button
					>
				</div>
			</Card>
			{#if preview}
				<Card
					title="Update preview"
					subtitle="Review the candidates before applying updates."
				>
					{#each preview.targets as target (target.policyId)}
						<h3>
							{environmentName(envs.data, target.environmentId)} / {targetName(
								target.type,
								target.id
							)}
						</h3>
						{#if target.sourceDrift}<p>
								Deploy this stack's source changes before updating.
							</p>{/if}
						<ul>
							{#each target.items as item (item.id)}<li>
									{item.service}: {item.status}
								</li>{/each}
						</ul>
					{/each}
					<Button variant="primary" onclick={run} disabled={busy}>Apply updates</Button>
				</Card>
			{/if}
			<Card title="Delete policy">
				{#if confirmDelete}<p>Delete {p.name} and stop its automatic checks and updates?</p>
					<Button onclick={remove} disabled={busy}>Confirm delete</Button>
				{:else}<Button onclick={() => (confirmDelete = true)}>Delete policy</Button>{/if}
			</Card>
		{/snippet}
	</QueryView>
</Page>

<style>
	.actions {
		display: flex;
		gap: 0.75rem;
		margin-top: 1rem;
	}
	h3 {
		margin-top: 1rem;
	}
</style>
