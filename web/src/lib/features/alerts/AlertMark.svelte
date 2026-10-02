<script lang="ts">
	// The mark beside a disk or RAID array with a firing alert (#159, the
	// System tab): "Alert" in the severity's tone, or "Alert Dismissed"
	// while someone dismissed it, the alert's title as tooltip. It opens
	// Alerts filtered to this environment, the alert's kind and its state
	// (Active or Dismissed).
	import { presetFilters } from '$lib/features/dashboard/AttentionStrip.svelte';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { Badge } from '$lib/ui';
	import { alertsPreset } from './filters';
	import { alertView, severityTone, type Alert } from './model';

	let {
		alert
	}: {
		alert: Pick<Alert, 'kind' | 'severity' | 'state' | 'dismissed' | 'title' | 'environmentId'>;
	} = $props();

	const view = $derived(alertView(alert));
	const text = $derived(view === 'dismissed' ? 'Alert Dismissed' : 'Alert');

	function open() {
		const p = alertsPreset(
			{ kind: alert.kind, view, environmentId: alert.environmentId },
			environmentSelection.id
		);
		if (p.select) environmentSelection.select(p.select);
		presetFilters(p);
	}
</script>

<a
	class="mark"
	href={routes.alerts()}
	title={alert.title}
	aria-label="{text}: {alert.title}"
	onclick={open}
>
	<Badge tone={view === 'dismissed' ? 'neutral' : severityTone(alert.severity)} dot>{text}</Badge>
</a>

<style>
	.mark {
		display: inline-flex;
		margin-left: var(--space-2);
		vertical-align: middle;
		border-radius: var(--radius-sm);
		text-decoration: none;
	}

	.mark:hover :global(.badge) {
		border-color: var(--text-faint);
		color: var(--text-strong);
	}
</style>
