<script lang="ts">
	// Move complete (the new manager, owner; docs/internal/architecture/manager-move.md):
	// what is left after Docker Manager moved here, in the order to do it:
	// the old manager's confirmation (or "Mark as done" once it failed),
	// agents that did not get the new address with their one-line fix, the
	// moved apps' stopped copies on the old server (removed as the
	// environment migration wizard does: one source removal per stack, one
	// summary toast) and archiving the old server's environment. The owner's
	// dashboard shows it until everything is done (moveCompleteDone); so
	// does Settings → Move to a new server.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import Circle from '@lucide/svelte/icons/circle';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { oldCopies } from '$lib/features/environments/environment-migration';
	import { removeOldCopies } from '$lib/features/environments/migration-actions';
	import { environmentMigrationQuery } from '$lib/features/environments/queries';
	import { bulkSummary } from '$lib/features/resources/bulk';
	import { count } from '$lib/features/stacks/migration';
	import { stackKeys } from '$lib/features/stacks/queries';
	import { routes } from '$lib/routes';
	import { Button, Card, ConfirmDialog, CopyButton, DestructiveConfirm, toast } from '$lib/ui';
	import { canAcknowledge, completeItems, type ManagerMove } from './model';
	import { acknowledgeConfirmation, managerMoveKeys } from './queries';

	let { move }: { move: ManagerMove } = $props();

	const qc = useQueryClient();
	const items = $derived(completeItems(move));
	const old = $derived(move.oldEnvironment);

	// The environment migration that moved the apps: its moved stacks' copies.
	const record = createQuery(() => ({
		...environmentMigrationQuery(old?.environmentId ?? '', old?.migrationId ?? ''),
		enabled: !!old?.migrationId && (old?.stoppedCopies ?? 0) > 0
	}));
	const copies = $derived(record.data ? oldCopies(record.data) : []);

	let acknowledging = $state(false);
	let removing = $state(false);
	let removal = $state<'idle' | 'running'>('idle');

	async function acknowledge() {
		const m = await acknowledgeConfirmation();
		qc.setQueryData(managerMoveKeys.current, m);
		void qc.invalidateQueries({ queryKey: managerMoveKeys.current });
		toast.success('Marked the move as done');
	}

	function removeCopies() {
		const list = copies;
		const name = old?.name ?? 'the old server';
		removal = 'running';
		void removeOldCopies(list).then((o) => {
			removal = 'idle';
			const s = bulkSummary('remove', { one: 'old copy', many: 'old copies' }, o);
			const title = o.succeeded.length ? `${s.title} from ${name}` : s.title;
			toast[s.tone](title, s.body ? { body: s.body } : undefined);
			void qc.invalidateQueries({ queryKey: managerMoveKeys.current });
			void qc.invalidateQueries({ queryKey: stackKeys.all });
			void record.refetch();
		});
	}
</script>

<Card
	title="Move complete"
	subtitle={move.sourceUrl
		? `Docker Manager moved here from ${move.sourceUrl}. A few things are left to do.`
		: 'Docker Manager moved here. A few things are left to do.'}
>
	<ul class="items" role="list" aria-label="Left to do after the move">
		{#each items as it (it.id)}
			<li class:done={it.done}>
				<span class="mark" aria-hidden="true">
					{#if it.done}<CircleCheck size={18} strokeWidth={1.75} />{:else}<Circle
							size={18}
							strokeWidth={1.75}
						/>{/if}
				</span>
				<div class="body">
					<p class="title">
						{it.title}<span class="sr-only">{it.done ? ' (done)' : ''}</span>
					</p>
					{#if it.body}<p class="muted">{it.body}</p>{/if}
					{#if it.fix}
						<div class="fix">
							<code class="mono">{it.fix}</code>
							<CopyButton value={it.fix} what="setting" />
						</div>
					{/if}
					{#if it.id === 'confirm' && canAcknowledge(move)}
						<div class="act">
							<Button size="sm" onclick={() => (acknowledging = true)}
								>Mark as done</Button
							>
						</div>
					{:else if it.id === 'copies' && !it.done && old}
						<div class="act">
							{#if removal === 'running'}
								<span class="muted" role="status">Removing the old copies…</span>
							{:else if copies.length}
								<Button
									size="sm"
									variant="danger-soft"
									icon={Trash2}
									onclick={() => (removing = true)}
									>Remove old copies from {old.name}</Button
								>
							{:else}
								<Button size="sm" href={routes.environment(old.environmentId)}
									>Open {old.name}</Button
								>
							{/if}
						</div>
					{:else if it.id === 'archive' && !it.done && old}
						<div class="act">
							<Button
								size="sm"
								icon={Archive}
								href={routes.environment(old.environmentId)}
								>Open {old.name} to archive it</Button
							>
						</div>
					{/if}
				</div>
			</li>
		{/each}
	</ul>
</Card>

<ConfirmDialog
	bind:open={acknowledging}
	title="Mark the move as done?"
	consequences={[
		'Docker Manager stops asking the old server to confirm the move.',
		'Only do this when Docker Manager on the old server is stopped or no longer in use. Two copies must never manage the same servers.'
	]}
	confirmLabel="Mark as done"
	tone="danger"
	onconfirm={acknowledge}
/>

{#if old}
	<DestructiveConfirm
		bind:open={removing}
		title="Remove the old copies from {old.name}?"
		consequences={[
			`Removes the stopped containers and networks of ${count(copies.length, 'stack')} on ${old.name}.`,
			`Deletes their moved volumes and their project folders there.`,
			`Backups of ${old.name} stay in their repository and remain restorable.`
		]}
		affected={copies.map((c) => ({ label: c.title, detail: 'stopped copy' }))}
		confirmText={old.name}
		confirmLabel="Remove old copies"
		onconfirm={removeCopies}
	/>
{/if}

<style>
	.items {
		display: grid;
		gap: var(--space-3);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.items li {
		display: flex;
		align-items: flex-start;
		gap: var(--space-3);
	}

	.mark {
		display: grid;
		flex: none;
		place-items: center;
		margin-top: 1px;
		color: var(--text-muted);
	}

	.done .mark {
		color: var(--ok);
	}

	.body {
		display: grid;
		gap: var(--space-1);
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
	}

	.done .title {
		color: var(--text-muted);
	}

	.fix {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.fix code {
		padding: 2px var(--space-2);
		border-radius: var(--radius-sm);
		background: var(--code-bg);
		overflow-wrap: anywhere;
	}

	.act {
		margin-top: var(--space-1);
	}
</style>
