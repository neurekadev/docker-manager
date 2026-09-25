<script lang="ts">
	// The members of a backup set (#10): each stack, volume or the manager
	// state with its own state and snapshot time. Multi-host sets are not
	// atomic, so every member shows the time its environment took it.
	import { Badge, formatDateTime } from '$lib/ui';
	import { routes } from '$lib/routes';
	import { itemName, memberState, type SetMember } from './model';

	let {
		members,
		environmentName
	}: { members: SetMember[]; environmentName?: (id: string) => string } = $props();
</script>

<ul class="members" role="list">
	{#each members as m, i (`${m.scope}-${m.item}-${i}`)}
		{@const s = memberState(m.state)}
		<li>
			<span class="name">{itemName(m)}</span>
			{#if m.environmentId && environmentName}<span class="muted"
					>{environmentName(m.environmentId)}</span
				>{/if}
			<Badge tone={s.tone} dot>{s.label}</Badge>
			{#if m.snapshotTime}
				<span class="muted num">{formatDateTime(m.snapshotTime)}</span>
			{/if}
			{#if m.errorClass}<span class="err">{m.errorClass.replaceAll('_', ' ')}</span>{/if}
			{#if m.jobId}<a class="job" href={routes.job(m.jobId)}>Job</a>{/if}
		</li>
	{/each}
</ul>

<style>
	.members {
		display: grid;
		gap: var(--space-1);
	}

	li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-2);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.err {
		color: var(--danger);
	}
</style>
