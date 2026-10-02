<script lang="ts">
	// Details of one RAID array (#206), opened from the RAID card's info
	// button: its state, level, size, disks, superblock, chunk size,
	// layout, bitmap and running sync, then every member by path with its
	// slot, role and the health of the disk it lives on (matched against
	// the Disk Health card's disks). A ZFS pool reports only its health.
	import Facts, { type Fact } from '$lib/features/common/Facts.svelte';
	import { Button, Dialog, StatusBadge, Table, formatBytes, type Column } from '$lib/ui';
	import {
		arrayName,
		devPath,
		diskBadge,
		memberDisk,
		memberRole,
		raidBadge,
		raidBitmap,
		raidDisks,
		raidLayout,
		raidLevel,
		raidProgress,
		sortMembers,
		type DiskDevice,
		type RaidArray,
		type RaidMember
	} from './diskHealth';

	let {
		open = $bindable(false),
		array,
		devices = []
	}: {
		open?: boolean;
		array: RaidArray;
		/** The environment's disks, for the members' disk health. */
		devices?: DiskDevice[];
	} = $props();

	const name = $derived(arrayName(array));
	const members = $derived(sortMembers(array.members));
	const md = $derived(array.kind === 'md');

	const facts = $derived<Fact[]>(
		md
			? [
					{ label: 'State', render: stateFact },
					{ label: 'Level', value: raidLevel(array) },
					{ label: 'Size', value: array.sizeBytes ? formatBytes(array.sizeBytes) : '' },
					{ label: 'Disks', value: raidDisks(array) },
					{ label: 'Metadata', value: array.metadata, mono: true },
					{
						label: 'Chunk Size',
						value: array.chunkBytes ? formatBytes(array.chunkBytes) : ''
					},
					{ label: 'Layout', value: raidLayout(array) },
					{ label: 'Write-Intent Bitmap', value: raidBitmap(array) },
					{ label: 'Read-Only', value: array.readOnly ? 'Yes' : 'No' },
					{ label: 'Sync', value: raidProgress(array)?.text ?? 'None' }
				]
			: [
					{ label: 'State', render: stateFact },
					{ label: 'Kind', value: raidLevel(array) },
					{ label: 'Pool Health', value: array.health, mono: true }
				]
	);

	const columns: Column<RaidMember>[] = [
		{
			id: 'device',
			header: 'Device',
			cell: deviceCell,
			mono: true,
			sortValue: (m) => m.name,
			stack: 'title'
		},
		{
			id: 'slot',
			header: 'Slot',
			cell: slotCell,
			numeric: true,
			width: '64px',
			sortValue: (m) => m.slot
		},
		{ id: 'role', header: 'Role', cell: roleCell, stack: 'status' },
		{ id: 'disk', header: 'Disk Health', cell: diskCell }
	];
</script>

{#snippet stateFact()}
	{@const b = raidBadge(array)}
	<StatusBadge status={b.status} label={b.label} />
{/snippet}
{#snippet deviceCell(m: RaidMember)}{devPath(m.name)}{/snippet}
{#snippet slotCell(m: RaidMember)}{m.slot}{/snippet}
{#snippet roleCell(m: RaidMember)}
	<span class:failed={m.state === 'failed'}>{memberRole(m)}</span>
{/snippet}
{#snippet diskCell(m: RaidMember)}
	{@const d = memberDisk(m, devices)}
	{#if d}
		{@const b = diskBadge(d)}
		<span class="disk"
			><StatusBadge status={b.status} label={b.label} title={b.title} /><span
				class="muted mono">{d.name}</span
			></span
		>
	{:else}<span class="muted">—</span>{/if}
{/snippet}

<Dialog bind:open title={name} description={raidLevel(array)} size="lg">
	<div class="body">
		<Facts items={facts} />
		{#if md}
			<section>
				<h3 class="subsection-title">Members</h3>
				{#if members.length}
					<div class="table">
						<Table
							label="Members of {name}"
							rows={members}
							{columns}
							rowKey={(m) => `${m.name}/${m.slot}`}
						/>
					</div>
				{:else}
					<p class="muted empty">The array lists no members.</p>
				{/if}
			</section>
		{:else}
			<p class="muted empty">ZFS reports only the pool’s health, not its disks.</p>
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

	.disk {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
	}

	.failed {
		color: var(--danger);
		font-weight: var(--weight-medium);
	}

	.empty {
		margin: 0;
	}
</style>
