<script lang="ts">
	// One backup policy on the Backups overview (#10): what it covers and
	// where, when it runs next, how its last set went, and a manual run.
	import { useQueryClient } from '@tanstack/svelte-query';
	import Play from '@lucide/svelte/icons/play';
	import { goto } from '$app/navigation';
	import { api, unwrap } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { Badge, Button, formatDateTime, formatRelative, toast } from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import { retentionText, scopeText, setState, type BackupPolicy } from './model';

	let {
		policy: p,
		repositoryName,
		environmentName
	}: {
		policy: BackupPolicy;
		repositoryName?: string;
		environmentName: (id: string) => string;
	} = $props();

	const qc = useQueryClient();
	let running = $state(false);
	const last = $derived(p.recentSets?.[0]);
	// Minimal views (#17) carry no exclusions.
	const excluded = $derived((p.excludeStacks ?? []).length + (p.excludeVolumes ?? []).length);

	async function run() {
		running = true;
		try {
			const out = await unwrap(
				api.POST('/api/v1/backup-policies/{policyId}/runs', {
					params: {
						path: { policyId: p.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: {}
				})
			);
			void qc.invalidateQueries({ queryKey: ['policies'] });
			toast.info(`Started a backup of ${p.name}`, {
				body: `${out.jobs.length} ${out.jobs.length === 1 ? 'job' : 'jobs'}`,
				action: {
					label: 'Open policy',
					onclick: () => void goto(routes.backupPolicy(p.id))
				}
			});
		} catch (e) {
			toast.error(`${p.name} was not backed up`, {
				body: actionError(e, {
					recovery_key_not_confirmed:
						'Confirm the Recovery Key of the repositories this policy uses first.'
				})
			});
		} finally {
			running = false;
		}
	}
</script>

<article class="policy" aria-labelledby="policy-{p.id}">
	<header class="head">
		<a id="policy-{p.id}" class="name" href={routes.backupPolicy(p.id)}>{p.name}</a>
		{#if p.schedule?.enabled}<Badge tone="ok" dot>Scheduled</Badge>{:else}<Badge dot
				>Manual</Badge
			>{/if}
	</header>
	<p class="scope">
		Backs up {scopeText(p, environmentName)}{repositoryName
			? ` to ${repositoryName}`
			: ''}{excluded ? `, ${excluded} ${excluded === 1 ? 'exclusion' : 'exclusions'}` : ''}.
	</p>
	<dl class="facts">
		<div>
			<dt>Last set</dt>
			<dd>
				{#if last}
					{@const st = setState(last.state)}
					<Badge tone={st.tone} dot>{st.label}</Badge>
					<span class="muted num" title={formatDateTime(last.startedAt)}
						>{formatRelative(last.startedAt)}</span
					>
				{:else}<span class="muted">Never run</span>{/if}
			</dd>
		</div>
		<div>
			<dt>Next run</dt>
			<dd>
				{#if p.schedule?.enabled && p.schedule.nextRun}<span
						class="num"
						title={formatDateTime(p.schedule.nextRun)}
						>{formatRelative(p.schedule.nextRun)}</span
					>{:else}<span class="muted">Only when started</span>{/if}
			</dd>
		</div>
		<div class="wide">
			<dt>Retention</dt>
			<dd>{retentionText(p.retention)}</dd>
		</div>
	</dl>
	<footer class="foot">
		<Button size="sm" variant="ghost" href={routes.backupPolicy(p.id)}>Open</Button>
		{#if has(p, 'backup.run')}
			<Button size="sm" icon={Play} loading={running} onclick={run}>Back up now</Button>
		{/if}
	</footer>
</article>

<style>
	.policy {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		min-width: 0;
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.scope {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.facts {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-3);
		margin: 0;
	}

	.facts .wide {
		grid-column: 1 / -1;
	}

	dt {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	dd {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin: 2px 0 0;
	}

	.foot {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-2);
		margin-top: auto;
	}
</style>
