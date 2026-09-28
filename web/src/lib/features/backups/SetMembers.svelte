<script lang="ts">
	// The members of a backup set (#10): each stack, volume or the manager
	// state with its own state and snapshot time, in aligned columns.
	// Multi-host sets are not atomic, so every member shows the time its
	// environment took it. With the set's backups given, members link to
	// their backup (members carry no backup ID; memberBackup matches them).
	import { Badge, formatDateTime } from '$lib/ui';
	import { routes } from '$lib/routes';
	import {
		itemName,
		memberBackup,
		memberState,
		sentenceCase,
		type Backup,
		type SetMember
	} from './model';

	let {
		members,
		environmentName,
		backups,
		currentId
	}: {
		members: SetMember[];
		environmentName?: (id: string) => string;
		/** The set's backups: members link to theirs. */
		backups?: Backup[];
		/** The backup being shown (not linked). */
		currentId?: string;
	} = $props();
</script>

<table class="members">
	<thead>
		<tr>
			<th scope="col">Backup</th>
			<th scope="col">State</th>
			<th scope="col">Taken</th>
			<th scope="col"><span class="sr-only">Job</span></th>
		</tr>
	</thead>
	<tbody>
		{#each members as m, i (`${m.scope}-${m.item}-${i}`)}
			{@const s = memberState(m.state)}
			{@const b = memberBackup(m, backups)}
			<tr>
				<td class="name">
					{#if b && b.id !== currentId}
						<a href={routes.backup(b.id)}>{itemName(m)}</a>
					{:else}
						<span
							class="strong"
							aria-current={b && b.id === currentId ? 'page' : undefined}
							>{itemName(m)}</span
						>
					{/if}
					{#if m.environmentId && environmentName}<span class="muted sub"
							>{environmentName(m.environmentId)}</span
						>{/if}
					{#if m.errorClass}<span class="err sub"
							>{sentenceCase(m.errorClass.replaceAll('_', ' '))}</span
						>{/if}
				</td>
				<td><Badge tone={s.tone} dot>{s.label}</Badge></td>
				<td class="muted num">{m.snapshotTime ? formatDateTime(m.snapshotTime) : '—'}</td>
				<td class="job">
					{#if m.jobId}<a href={routes.job(m.jobId)}>Job</a>{/if}
				</td>
			</tr>
		{/each}
	</tbody>
</table>

<style>
	.members {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	th {
		padding: 0 var(--space-3) var(--space-1) 0;
		color: var(--text-muted);
		font-weight: var(--weight-medium);
		text-align: left;
	}

	td {
		padding: var(--space-1) var(--space-3) var(--space-1) 0;
		border-top: 1px solid var(--border-subtle);
		vertical-align: middle;
		white-space: nowrap;
	}

	td.name {
		width: 100%;
		white-space: normal;
		overflow-wrap: anywhere;
	}

	.strong {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.sub {
		display: block;
	}

	.err {
		color: var(--danger);
	}

	.job {
		padding-right: 0;
		text-align: right;
	}
</style>
