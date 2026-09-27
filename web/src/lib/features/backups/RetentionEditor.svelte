<script lang="ts">
	// Retention of a backup policy (#10): keep rules like restic's, a
	// minimum recovery floor per stack/volume (the newest is never
	// forgotten), and a preview of exactly which snapshots would go. Docker Manager
	// computes the decision, so the preview and the run agree.
	import { Switch, TextField } from '$lib/ui';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import RetentionPreviewPanel from './RetentionPreviewPanel.svelte';
	import { hasRetentionRules, retentionText, type BackupRetention } from './model';

	interface Props {
		value: BackupRetention;
		/** Saved policy to preview against (the unsaved rules are sent along). */
		policyId?: string;
		onchange?: () => void;
	}

	let { value = $bindable(), policyId, onchange }: Props = $props();

	const RULES: { key: keyof BackupRetention; label: string; hint: string }[] = [
		{ key: 'last', label: 'Last', hint: 'Newest backups' },
		{ key: 'hourly', label: 'Hourly', hint: 'One per hour' },
		{ key: 'daily', label: 'Daily', hint: 'One per day' },
		{ key: 'weekly', label: 'Weekly', hint: 'One per week' },
		{ key: 'monthly', label: 'Monthly', hint: 'One per month' },
		{ key: 'yearly', label: 'Yearly', hint: 'One per year' },
		{ key: 'withinDays', label: 'Everything from the last', hint: 'Days' }
	];

	// 0 turns a rule off; an emptied field counts as 0.
	function num(key: keyof BackupRetention, v: string) {
		const n = Math.max(0, Math.floor(Number(v.trim() || '0')));
		value = { ...value, [key]: Number.isFinite(n) ? n : 0 };
		onchange?.();
	}

	const floorError = $derived(
		hasRetentionRules(value) && (value.minKeep ?? 0) < 1
			? 'Keep at least 1 per stack and volume when rules are set.'
			: null
	);
</script>

<div class="retention">
	<FieldGroup
		legend="Keep"
		hint="0 turns a rule off; with every rule at 0 every backup is kept. Rules combine: a backup kept by any rule stays."
	>
		<div class="grid">
			{#each RULES as r (r.key)}
				<TextField
					label={r.label}
					type="number"
					min="0"
					description={r.hint}
					value={String(value[r.key] ?? 0)}
					onchange={(e) => num(r.key, e.currentTarget.value)}
				/>
			{/each}
		</div>
	</FieldGroup>
	<TextField
		label="Minimum recovery floor"
		type="number"
		min="0"
		description="Always keep at least this many of the newest backups of each stack and volume, whatever the rules say."
		value={String(value.minKeep ?? 0)}
		error={floorError}
		onchange={(e) => num('minKeep', e.currentTarget.value)}
	/>
	<Switch
		label="Apply retention after every backup"
		description="Off: apply it from the policy page when you want."
		checked={!!value.afterBackup}
		onchange={(v) => {
			value = { ...value, afterBackup: v };
			onchange?.();
		}}
	/>
	<p class="summary">{retentionText(value)}.</p>
	{#if policyId}
		<RetentionPreviewPanel {policyId} retention={value} disabled={!!floorError} />
	{/if}
</div>

<style>
	.retention {
		display: grid;
		gap: var(--space-4);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
		gap: var(--space-3);
	}

	.summary {
		color: var(--text-strong);
	}
</style>
