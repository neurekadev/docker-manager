<script lang="ts">
	// Details of one disk (#206), opened from the Disk health card's info
	// button: what identifies it, its health and read time, then every ATA
	// attribute as smartctl reads it (value, worst, threshold, raw, type,
	// status) and the other health values the disk reports (the NVMe health
	// log, SCSI error counters, the power cycle count), in smartctl's order.
	// Every value that says something about the disk's health carries an
	// OK, warning or danger mark (SmartCheckMark, #210); informational ones
	// none. An agent that predates the full read sends neither table.
	import Facts, { type Fact } from '$lib/features/common/Facts.svelte';
	import {
		Button,
		Dialog,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		formatTemperature,
		type Column
	} from '$lib/ui';
	import SmartCheckMark from './SmartCheckMark.svelte';
	import {
		attributeCheck,
		attributeRaw,
		attributeType,
		capacity,
		diskBadge,
		diskKind,
		healthValue,
		issuesText,
		hotTimeCheck,
		poweredOn,
		selfAssessmentCheck,
		temperatureCheck,
		valueCheck,
		wearCheck,
		type SmartCheck,
		type DiskAttributeRow,
		type DiskDevice,
		type DiskValue
	} from './diskHealth';

	let {
		open = $bindable(false),
		disk,
		name,
		now
	}: {
		open?: boolean;
		disk: DiskDevice;
		/** The device's name as the card shows it (deviceName). */
		name: string;
		now?: Date;
	} = $props();

	const attributes = $derived(disk.attributes ?? []);
	const values = $derived(disk.values ?? []);

	const facts = $derived<Fact[]>([
		{ label: 'Health', render: healthFact },
		{ label: 'Issues', value: issuesText(disk) },
		{ label: 'Model', value: disk.model },
		{ label: 'Serial Number', value: disk.serial, mono: true },
		{ label: 'Firmware', value: disk.firmware, mono: true },
		{ label: 'Capacity', value: capacity(disk) },
		{ label: 'Kind', value: diskKind(disk) },
		{ label: 'Device Type', value: disk.type, mono: true },
		{ label: 'Self-Assessment', render: selfAssessmentFact },
		temperatureCheck(disk)
			? { label: 'Temperature', render: temperatureFact }
			: {
					label: 'Temperature',
					value:
						disk.temperatureC === undefined ? '' : formatTemperature(disk.temperatureC)
				},
		...(hotTimeCheck(disk) ? [{ label: 'Time Above Its Limit', render: hotTimeFact }] : []),
		...(wearCheck(disk) ? [{ label: 'Wear', render: wearFact }] : []),
		{ label: 'Powered On', value: poweredOn(disk) === '—' ? '' : poweredOn(disk) },
		{ label: 'Last Read', render: readFact }
	]);

	const attributeColumns: Column<DiskAttributeRow>[] = [
		{ id: 'id', header: 'ID', cell: idCell, numeric: true, width: '56px', stack: 'hidden' },
		{
			id: 'name',
			header: 'Attribute',
			cell: nameCell,
			mono: true,
			maxWidth: '260px',
			truncate: true,
			title: (a) => a.name,
			stack: 'title'
		},
		{ id: 'value', header: 'Value', cell: valueCell, numeric: true, width: '72px' },
		{ id: 'worst', header: 'Worst', cell: worstCell, numeric: true, width: '72px' },
		{ id: 'threshold', header: 'Threshold', cell: thresholdCell, numeric: true, width: '96px' },
		{
			id: 'raw',
			header: 'Raw',
			cell: rawCell,
			mono: true,
			maxWidth: '200px',
			truncate: true,
			title: (a) => attributeRaw(a)
		},
		{ id: 'type', header: 'Type', cell: typeCell, width: '96px' },
		{ id: 'status', header: 'Status', cell: statusCell, width: '210px', stack: 'status' }
	];
	const valueColumns: Column<DiskValue>[] = [
		{ id: 'label', header: 'Value', cell: labelCell, stack: 'title' },
		{ id: 'value', header: 'Reading', cell: readingCell, numeric: true },
		{ id: 'status', header: 'Status', cell: valueStatusCell, width: '210px', stack: 'status' }
	];
</script>

{#snippet dash()}<span class="muted">—</span>{/snippet}
{#snippet healthFact()}
	{@const b = diskBadge(disk)}
	<StatusBadge status={b.status} label={b.label} title={b.title} />
{/snippet}
{#snippet selfAssessmentFact()}
	{@const c = selfAssessmentCheck(disk)}
	{#if c}<SmartCheckMark check={c} />{:else}{@render dash()}{/if}
{/snippet}
{#snippet checkMark(c: SmartCheck | null)}
	{#if c}<SmartCheckMark check={c} />{:else}{@render dash()}{/if}
{/snippet}
{#snippet temperatureFact()}{@render checkMark(temperatureCheck(disk))}{/snippet}
{#snippet hotTimeFact()}{@render checkMark(hotTimeCheck(disk))}{/snippet}
{#snippet wearFact()}{@render checkMark(wearCheck(disk))}{/snippet}
{#snippet readFact()}
	{#if disk.readAt}<time datetime={disk.readAt} title={formatDateTime(disk.readAt)}
			>{formatRelative(disk.readAt, now)}</time
		>{:else}{@render dash()}{/if}
{/snippet}
{#snippet idCell(a: DiskAttributeRow)}{a.id}{/snippet}
{#snippet nameCell(a: DiskAttributeRow)}{a.name}{/snippet}
{#snippet valueCell(
	a: DiskAttributeRow
)}{#if a.value !== undefined}{a.value}{:else}{@render dash()}{/if}{/snippet}
{#snippet worstCell(
	a: DiskAttributeRow
)}{#if a.worst !== undefined}{a.worst}{:else}{@render dash()}{/if}{/snippet}
{#snippet thresholdCell(
	a: DiskAttributeRow
)}{#if a.threshold !== undefined}{a.threshold}{:else}{@render dash()}{/if}{/snippet}
{#snippet rawCell(a: DiskAttributeRow)}{attributeRaw(a)}{/snippet}
{#snippet typeCell(a: DiskAttributeRow)}{attributeType(a)}{/snippet}
{#snippet statusCell(a: DiskAttributeRow)}
	{@const c = attributeCheck(a)}
	{#if c}<SmartCheckMark check={c} />{:else}{@render dash()}{/if}
{/snippet}
{#snippet labelCell(v: DiskValue)}<span title={v.key}>{healthValue(v).label}</span>{/snippet}
{#snippet readingCell(v: DiskValue)}{healthValue(v).text}{/snippet}
{#snippet valueStatusCell(v: DiskValue)}
	{@const c = valueCheck(v, disk)}
	{#if c}<SmartCheckMark check={c} />{:else}{@render dash()}{/if}
{/snippet}

<Dialog bind:open title={name} description={disk.model} size="xl">
	<div class="body">
		<Facts items={facts} columns={3} />
		{#if attributes.length}
			<section>
				<h3 class="subsection-title">SMART Attributes</h3>
				<div class="table">
					<Table
						label="SMART Attributes of {name}"
						rows={attributes}
						columns={attributeColumns}
						rowKey={(a) => String(attributes.indexOf(a))}
					/>
				</div>
			</section>
		{/if}
		{#if values.length}
			<section>
				<h3 class="subsection-title">Health Values</h3>
				<div class="table">
					<Table
						label="Health Values of {name}"
						rows={values}
						columns={valueColumns}
						rowKey={(v) => v.key}
					/>
				</div>
			</section>
		{/if}
		{#if !attributes.length && !values.length}
			<p class="muted empty">
				{disk.state === 'error' || !disk.readAt
					? 'No SMART values were read from this disk.'
					: 'No detailed SMART values reported. Older agents don’t send them; update the agent.'}
			</p>
		{/if}
	</div>
	{#snippet footer()}
		<Button variant="secondary" onclick={() => (open = false)}>Close</Button>
	{/snippet}
</Dialog>

<style>
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--space-5);
	}

	section {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}

	h3 {
		margin: 0;
	}

	.table {
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		overflow: hidden;
	}

	.empty {
		margin: 0;
	}
</style>
