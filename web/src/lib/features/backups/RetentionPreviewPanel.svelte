<script lang="ts">
	// Which snapshots a retention would forget (#10), per location, with the
	// rules that keep each one. `retention` previews unsaved rules; without
	// it the policy's saved rules are used. Once shown, the preview follows
	// changes of the unsaved rules (after a short pause), so it never shows
	// a decision the rules no longer make. Nothing is forgotten here.
	import { untrack } from 'svelte';
	import { api, unwrap } from '$lib/api/client';
	import { Badge, Button, Notice, formatDateTime } from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import type { BackupRetention, RetentionPreview } from './model';

	interface Props {
		policyId: string;
		retention?: BackupRetention;
		disabled?: boolean;
		/** Load at once (confirmation dialogs). */
		auto?: boolean;
	}

	let { policyId, retention, disabled = false, auto = false }: Props = $props();

	let preview = $state<RetentionPreview | null>(null);
	let loading = $state(false);
	let error = $state<string | null>(null);

	// Only the newest request's answer is shown.
	let seq = 0;
	async function load() {
		const mine = ++seq;
		loading = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/backup-policies/{policyId}/retention-previews', {
					params: { path: { policyId } },
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
</script>

<div class="panel">
	{#if !auto}
		<div><Button onclick={open} {loading} {disabled}>Preview retention</Button></div>
	{/if}
	{#if error}<Notice tone="danger" title="The preview could not be computed" live="alert"
			>{error}</Notice
		>{/if}
	{#if preview}
		{#if preview.locations.length === 0}
			<p class="muted">No backups yet: nothing would be forgotten.</p>
		{/if}
		{#each preview.locations as loc (loc.repositoryId + loc.scope)}
			<div class="loc">
				<p class="head">
					<strong>{loc.scope}</strong>
					<Badge tone={loc.forget ? 'danger' : 'ok'}>{loc.forget} forgotten</Badge>
					<Badge tone="ok">{loc.keep} kept</Badge>
				</p>
				<ul role="list" class="decisions">
					{#each loc.decisions as d (d.snapshotId)}
						<li>
							{#if d.keep}<Badge tone="ok" dot>Keep</Badge>{:else}<Badge
									tone="danger"
									dot>Forget</Badge
								>{/if}
							<span>{d.item}</span>
							<span class="num muted">{formatDateTime(d.time)}</span>
							{#if d.keep && d.reasons?.length}<span class="muted small"
									>kept by {d.reasons.join(', ')}</span
								>{:else if d.reasons?.includes('deleted')}<span class="muted small"
									>its stack or volume was deleted</span
								>{/if}
						</li>
					{/each}
				</ul>
			</div>
		{/each}
	{:else if loading && auto}
		<p class="muted">Computing the preview…</p>
	{/if}
</div>

<style>
	.panel,
	.loc {
		display: grid;
		gap: var(--space-2);
	}

	.head,
	.decisions li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.head strong {
		color: var(--text-strong);
	}

	.decisions {
		display: grid;
		gap: 2px;
		max-height: 280px;
		overflow: auto;
		font-size: var(--text-caption);
	}

	.small {
		font-size: var(--text-caption);
	}
</style>
