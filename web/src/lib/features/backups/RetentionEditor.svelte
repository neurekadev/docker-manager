<script lang="ts">
	// Retention of a backup policy (#10): a preset ("7 daily, 4 weekly, 12
	// monthly", the last 30, everything) or Custom, which reveals restic-like
	// keep rules ("Last" keeps the newest N of each stack and volume whatever
	// the others say). Removing the backups of deleted stacks and volumes is
	// a separate switch (off by default). The rule reads as a live
	// sentence, and a saved policy previews exactly which snapshots would go.
	// Docker Manager computes the decision, so the preview and the run agree.
	import { untrack } from 'svelte';
	import { RadioGroup, Switch, TextField } from '$lib/ui';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import RetentionPreviewPanel from './RetentionPreviewPanel.svelte';
	import {
		DEFAULT_EXPIRE_DELETED_DAYS,
		RETENTION_PRESETS,
		applyRetentionPreset,
		retentionPreset,
		retentionText,
		type BackupRetention,
		type RetentionPreset
	} from './model';

	interface Props {
		value: BackupRetention;
		/** Saved policy to preview against (the unsaved rules are sent along). */
		policyId?: string;
		onchange?: () => void;
	}

	let { value = $bindable(), policyId, onchange }: Props = $props();

	// Editing opens on the preset the saved rules match (else Custom).
	let preset = $state<RetentionPreset>(retentionPreset(untrack(() => value)));

	const RULES: { key: keyof BackupRetention; label: string; hint: string }[] = [
		{ key: 'last', label: 'Last', hint: 'Newest backups' },
		{ key: 'hourly', label: 'Hourly', hint: 'One per hour' },
		{ key: 'daily', label: 'Daily', hint: 'One per day' },
		{ key: 'weekly', label: 'Weekly', hint: 'One per week' },
		{ key: 'monthly', label: 'Monthly', hint: 'One per month' },
		{ key: 'yearly', label: 'Yearly', hint: 'One per year' },
		{ key: 'withinDays', label: 'Everything From the Last', hint: 'Days' }
	];

	function choose(p: string) {
		preset = p as RetentionPreset;
		value = applyRetentionPreset(preset, value);
		onchange?.();
	}

	// 0 turns a rule off; an emptied field counts as 0.
	function num(key: keyof BackupRetention, v: string) {
		const n = Math.max(0, Math.floor(Number(v.trim() || '0')));
		value = { ...value, [key]: Number.isFinite(n) ? n : 0 };
		onchange?.();
	}
</script>

<div class="retention">
	<div class="choice">
		<RadioGroup label="Keep" options={RETENTION_PRESETS} value={preset} onchange={choose} />
		<p class="summary" aria-live="polite">{retentionText(value)}.</p>
	</div>
	{#if preset === 'custom'}
		<FieldGroup legend="Rules" hint="0 turns a rule off. A backup kept by any rule stays.">
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
	{/if}
	<Switch
		label="Remove Backups of Deleted Stacks and Volumes"
		description="Otherwise the last backups of a deleted stack or volume are kept forever."
		info="Nothing counts as deleted while its server is offline."
		checked={!!value.expireDeletedDays}
		onchange={(v) => {
			value = { ...value, expireDeletedDays: v ? DEFAULT_EXPIRE_DELETED_DAYS : 0 };
			onchange?.();
		}}
	/>
	{#if value.expireDeletedDays}
		<TextField
			label="Remove Them After"
			type="number"
			min="1"
			description="Days since their newest backup."
			value={String(value.expireDeletedDays)}
			onchange={(e) => {
				const n = Math.max(1, Math.floor(Number(e.currentTarget.value.trim() || '1')));
				value = { ...value, expireDeletedDays: Number.isFinite(n) ? n : 1 };
				onchange?.();
			}}
		/>
	{/if}
	<Switch
		label="Apply Retention After Every Backup"
		checked={!!value.afterBackup}
		onchange={(v) => {
			value = { ...value, afterBackup: v };
			onchange?.();
		}}
	/>
	{#if policyId}
		<RetentionPreviewPanel {policyId} retention={value} />
	{/if}
</div>

<style>
	.retention {
		display: grid;
		gap: var(--space-4);
	}

	.choice {
		display: grid;
		gap: var(--space-2);
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
