<script lang="ts">
	// The findings of a migration check (#35): problems (danger) or
	// warnings, each with its title in words (findingTitle), the service it
	// concerns and the server's message. Shared by the stack and the
	// environment migration wizards.
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { sentence } from './migration';
	import { findingTitle, type MigrationFinding } from './model';

	interface Props {
		list: MigrationFinding[];
		tone: 'danger' | 'warn';
	}

	let { list, tone }: Props = $props();

	const findingKey = (f: MigrationFinding, i: number) =>
		`${f.code}/${f.service ?? ''}/${f.resource ?? ''}/${i}`;
</script>

<ul class="findings {tone}" role="list">
	{#each list as f, i (findingKey(f, i))}
		<li>
			{#if tone === 'danger'}<CircleAlert
					size={16}
					strokeWidth={1.75}
					aria-hidden="true"
				/>{:else}<TriangleAlert size={16} strokeWidth={1.75} aria-hidden="true" />{/if}
			<div>
				<span class="f-title">{findingTitle(f.code)}</span>
				{#if f.service}<span class="muted"> · {f.service}</span>{/if}
				<p class="f-msg">{sentence(f.message)}</p>
			</div>
		</li>
	{/each}
</ul>

<style>
	.findings {
		display: grid;
		gap: var(--space-2);
		margin: 0;
	}

	.findings li {
		display: flex;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
	}

	.findings.danger :global(svg) {
		flex: none;
		margin-top: 2px;
		color: var(--danger);
	}

	.findings.warn :global(svg) {
		flex: none;
		margin-top: 2px;
		color: var(--warn);
	}

	.f-title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.f-msg {
		color: var(--text-default);
	}
</style>
