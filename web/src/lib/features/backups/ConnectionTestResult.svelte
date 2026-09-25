<script lang="ts">
	// Result of a repository connection test (#10): read/write/delete on S3,
	// Object Lock (pruning may be refused), the restic repositories found
	// below the destination and whether the key opens them.
	import { Badge, Notice, formatRelative } from '$lib/ui';
	import type { ConnectionTest } from './model';

	let { test }: { test: ConnectionTest } = $props();

	const RESULT: Record<string, string> = {
		access_denied: 'The storage refused the credentials.',
		bucket_not_found: 'The bucket does not exist at this endpoint.',
		unreachable: 'The storage could not be reached.',
		path_not_allowed: 'The directory is outside DOCKYARD_BACKUP_LOCAL_ROOTS of its host.',
		path_not_writable: 'DockYard cannot write to the directory.',
		recovery_key_rejected: 'The Recovery Key does not open a repository found here.',
		repository_locked: 'A repository here is locked by another restic process.',
		storage_access_denied: 'The storage refused access.'
	};

	function can(v: boolean | undefined, label: string) {
		return { v, label };
	}
	const checks = $derived(
		[
			can(test.canRead, 'Read'),
			can(test.canWrite, 'Write'),
			can(test.canDelete, 'Delete')
		].filter((c) => c.v !== undefined)
	);
</script>

<div class="test">
	<div class="row">
		{#if test.ok}<Badge tone="ok" dot>Connection works</Badge>{:else}<Badge tone="danger" dot
				>Connection failed</Badge
			>{/if}
		{#each checks as c (c.label)}
			<Badge tone={c.v ? 'ok' : 'danger'}>{c.label}: {c.v ? 'yes' : 'no'}</Badge>
		{/each}
		<span class="muted num">Tested {formatRelative(test.at)}</span>
	</div>
	{#if !test.ok}
		<p class="danger">{RESULT[test.result] ?? test.message ?? test.result}</p>
	{/if}
	{#if test.objectLock}
		<Notice tone="warn" title="The bucket enforces Object Lock" live="none">
			Retention may be unable to delete old backups until their lock expires. Backups still
			work.
		</Notice>
	{/if}
	{#each test.warnings ?? [] as w (w)}<Notice tone="warn" title="Warning" live="none">{w}</Notice
		>{/each}
	{#if test.scopes?.length}
		<ul class="scopes" role="list">
			{#each test.scopes as s (s.scope)}
				<li>
					<span class="mono">{s.scope}</span>
					{#if !s.exists}<Badge>Not created yet</Badge>
					{:else if s.keyAccepted}<Badge tone="ok">Opens with the Recovery Key</Badge>
					{:else if s.previousKey}<Badge tone="warn">Still on the previous key</Badge>
					{:else}<Badge tone="danger">Key rejected</Badge>{/if}
					{#if s.errorClass}<span class="danger">{s.errorClass.replaceAll('_', ' ')}</span
						>{/if}
				</li>
			{/each}
		</ul>
	{/if}
</div>

<style>
	.test {
		display: grid;
		gap: var(--space-3);
	}

	.row,
	.scopes li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.scopes {
		display: grid;
		gap: var(--space-2);
	}

	.danger {
		color: var(--danger);
	}
</style>
