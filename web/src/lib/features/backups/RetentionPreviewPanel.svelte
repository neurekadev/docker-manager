<script lang="ts">
	// Which backups a retention would remove (#10). One line says what it
	// removes and keeps overall; then each repository location (repository
	// and environment by name) lists its stacks and volumes, one row each
	// with how many backups go and stay, those losing backups first. A row
	// opens to the backups with their dates and the rules keeping them.
	// `retention` previews unsaved rules; without it the saved rules are
	// used. It covers every Docker Manager backup, earlier policies' too.
	// Once shown, the preview follows changes of the unsaved rules (after a
	// short pause), so it never shows a decision the rules no longer make.
	// Nothing is removed here.
	import { untrack } from 'svelte';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import { api, unwrap } from '$lib/api/client';
	import { Badge, Button, Notice, Skeleton, formatDateTime } from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import {
		retentionGroups,
		retentionReasonText,
		scopeName,
		type BackupRetention,
		type RetentionPreview
	} from './model';

	interface Props {
		retention?: BackupRetention;
		disabled?: boolean;
		/** Load at once (confirmation dialogs). */
		auto?: boolean;
		environmentName?: (id: string) => string;
		repositoryName?: (id: string) => string | undefined;
		stackName?: (id: string) => string | undefined;
		/** Told whenever the preview changes: whether it is shown, and how many backups go. */
		onstate?: (s: { ready: boolean; forget: number }) => void;
	}

	let {
		retention,
		disabled = false,
		auto = false,
		environmentName = (id: string) => id,
		repositoryName,
		stackName,
		onstate
	}: Props = $props();

	let preview = $state<RetentionPreview | null>(null);
	let loading = $state(false);
	let error = $state<string | null>(null);

	const forget = $derived(preview?.locations.reduce((n, l) => n + l.forget, 0) ?? 0);
	const keep = $derived(preview?.locations.reduce((n, l) => n + l.keep, 0) ?? 0);
	$effect(() => {
		onstate?.({ ready: !!preview && !loading && !error, forget });
	});

	// Only the newest request's answer is shown.
	let seq = 0;
	async function load() {
		const mine = ++seq;
		loading = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/backup-settings/retention-previews', {
					body: retention ? { retention } : {}
				})
			);
			if (mine === seq) preview = out;
		} catch (e) {
			if (mine === seq) error = actionError(e);
		} finally {
			if (mine === seq) loading = false;
		}
	}

	// Opened by the button (or auto): later rule changes refresh it.
	let opened = false;
	function open() {
		opened = true;
		void load();
	}
	const rules = $derived(JSON.stringify(retention ?? null));
	$effect(() => {
		void rules;
		if (auto || disabled || !untrack(() => opened)) return;
		const t = setTimeout(() => void load(), 400);
		return () => clearTimeout(t);
	});

	$effect(() => {
		if (auto) void load();
	});

	const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
</script>

<div class="panel">
	{#if !auto}
		<div><Button onclick={open} {loading} {disabled}>Preview Retention</Button></div>
	{/if}
	{#if error}<Notice tone="danger" title="The preview could not be computed" live="alert"
			>{error}</Notice
		>{/if}
	{#if preview}
		{#if preview.locations.length === 0}
			<p class="muted">No backups yet: nothing would be removed.</p>
		{:else}
			<p class="summary" class:stale={loading}>
				{#if forget}
					Removes <strong class="num">{plural(forget, 'backup', 'backups')}</strong>,
					keeps
					<span class="num">{keep}</span>.
				{:else}
					Nothing to remove: the rules keep all {plural(keep, 'backup', 'backups')}.
				{/if}
			</p>
		{/if}
		{#each preview.locations as loc (loc.repositoryId + loc.scope)}
			<section class="loc" class:stale={loading}>
				<div class="loc-head">
					<strong>{repositoryName?.(loc.repositoryId) ?? 'Repository'}</strong>
					<span class="muted">{scopeName(loc.scope, environmentName)}</span>
					<span class="counts">
						{#if loc.forget}<Badge tone="danger" dot>{loc.forget} removed</Badge>{/if}
						<Badge tone="ok" dot>{loc.keep} kept</Badge>
					</span>
				</div>
				<ul class="items" role="list">
					{#each retentionGroups(loc.decisions, stackName) as g (g.item)}
						<li>
							<details>
								<summary>
									<ChevronRight size={14} aria-hidden="true" class="chev" />
									<span class="name">{g.name}</span>
									<span class="muted small num">
										{#if g.forget}<span class="gone">{g.forget} removed</span> ·
										{/if}{g.keep} kept
									</span>
								</summary>
								<ul class="decisions" role="list">
									{#each g.decisions as d (d.snapshotId)}
										<li>
											{#if d.keep}<Badge tone="ok" dot>Keep</Badge
												>{:else}<Badge tone="danger" dot>Remove</Badge>{/if}
											<span class="num">{formatDateTime(d.time)}</span>
											{#if d.keep && d.reasons?.length}<span class="muted"
													>kept by {d.reasons
														.map(retentionReasonText)
														.join(', ')}</span
												>{:else if d.reasons?.includes('deleted')}<span
													class="muted"
													>its stack or volume was deleted</span
												>{/if}
										</li>
									{/each}
								</ul>
							</details>
						</li>
					{/each}
				</ul>
			</section>
		{/each}
	{:else if loading && auto}
		<div aria-busy="true" aria-label="Computing the preview"><Skeleton lines={4} /></div>
	{/if}
</div>

<style>
	.panel {
		display: grid;
		gap: var(--space-3);
		min-width: 0;
	}

	.summary {
		color: var(--text-default);
	}

	.summary strong {
		color: var(--text-strong);
	}

	.stale {
		opacity: 0.6;
	}

	.loc {
		display: grid;
		gap: var(--space-2);
		min-width: 0;
	}

	.loc-head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-1) var(--space-2);
	}

	.loc-head strong,
	.name {
		color: var(--text-strong);
	}

	.counts {
		display: inline-flex;
		gap: var(--space-1);
		margin-left: auto;
	}

	.items {
		display: grid;
		max-height: 320px;
		margin: 0;
		padding: 0;
		overflow: auto;
		list-style: none;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}

	.items > li + li {
		border-top: 1px solid var(--border-subtle);
	}

	summary {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		padding: 6px var(--space-3);
		list-style: none;
		cursor: pointer;
	}

	summary::-webkit-details-marker {
		display: none;
	}

	summary :global(.chev) {
		flex: none;
		color: var(--text-muted);
		transition: transform var(--duration-fast) var(--ease-out);
	}

	details[open] > summary :global(.chev) {
		transform: rotate(90deg);
	}

	summary .small {
		margin-left: auto;
	}

	.gone {
		color: var(--danger);
	}

	.decisions {
		display: grid;
		gap: 2px;
		margin: 0;
		padding: 0 var(--space-3) var(--space-2) calc(var(--space-3) + 14px + var(--space-2));
		list-style: none;
		font-size: var(--text-caption);
	}

	.decisions li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.small {
		font-size: var(--text-caption);
	}
</style>
