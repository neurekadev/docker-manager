<script lang="ts">
	// Schedules (#13): the default time zone and the default expression per
	// kind that new policies start with (existing policies keep theirs), and
	// the cross-policy view is the Schedules page (/schedules).
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import { api, unwrap, type Schema } from '$lib/api/client';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		Combobox,
		CronField,
		DeniedState,
		Dialog,
		Notice,
		Table,
		toast,
		type Column
	} from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { ifMatch } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import {
		scheduleDefaultsKey,
		scheduleDefaultsQuery,
		type ScheduleDefaults
	} from '$lib/features/common/schedules';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';

	type Kind = Schema<'ScheduleKindDefault'>;

	usePage({
		title: 'Schedule defaults',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'Schedule defaults' }]
	});

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const canRead = $derived(can(access, 'settings.read'));
	const canEdit = $derived(can(access, 'settings.manage'));
	const defaults = createQuery(() => ({ ...scheduleDefaultsQuery(), enabled: canRead }));

	let zone = $state('');
	$effect(() => {
		const d = defaults.data;
		if (d) untrack(() => (zone ||= d.timeZone));
	});
	// Browsers omit aliases such as UTC from their list: keep the saved zone.
	const zones = $derived.by(() => {
		const list =
			typeof Intl.supportedValuesOf === 'function'
				? Intl.supportedValuesOf('timeZone')
				: ['UTC'];
		const saved = defaults.data?.timeZone;
		return (saved && !list.includes(saved) ? [saved, ...list] : list).map((z) => ({
			value: z,
			label: z
		}));
	});

	let editing = $state<Kind | null>(null);
	let editCron = $state('');
	let editZone = $state('UTC');
	let saveError = $state<string | null>(null);
	let saving = $state(false);

	async function patch(
		d: ScheduleDefaults,
		body: Schema<'PatchScheduleDefaultsInputBody'>,
		message: string
	) {
		saving = true;
		saveError = null;
		try {
			const saved = await unwrap(
				api.PATCH('/api/v1/schedule-defaults', {
					params: { header: { 'If-Match': ifMatch(d.revision) } },
					body
				})
			);
			qc.setQueryData(scheduleDefaultsKey, saved);
			toast.success(message, {
				body: 'New policies start with it; existing policies keep their schedules.'
			});
			editing = null;
		} catch (e) {
			saveError = actionError(e);
		} finally {
			saving = false;
		}
	}

	const kindColumns: Column<Kind>[] = [
		{ id: 'kind', header: 'Schedule', cell: kindCell, stack: 'title' },
		{ id: 'cron', header: 'Default', cell: cronCell, width: '160px' },
		{ id: 'catch', header: 'Missed runs', cell: catchCell, width: '220px' },
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: kindActions,
			width: '110px',
			stack: 'actions'
		}
	];
</script>

{#snippet kindCell(k: Kind)}<NameCell
		name={k.label}
		sub={k.cron === k.suggested ? 'Docker Manager’s suggestion' : `Suggested: ${k.suggested}`}
	/>{/snippet}
{#snippet cronCell(k: Kind)}<span class="mono">{k.cron}</span>{/snippet}
{#snippet catchCell(k: Kind)}<span class="muted"
		>{k.catchUp === 'once' ? 'One catch-up run' : 'Skipped and recorded'}</span
	>{/snippet}
{#snippet kindActions(k: Kind)}
	{#if canEdit}
		<Button
			size="sm"
			variant="ghost"
			onclick={() => {
				editing = k;
				editCron = k.cron;
				editZone = defaults.data?.timeZone ?? 'UTC';
				saveError = null;
			}}>Change</Button
		>
	{/if}
{/snippet}

<Page>
	<SettingsHeader
		title="Schedule defaults"
		description="What new backup, update and maintenance schedules start with. Existing policies keep their own."
	/>
	{#if perms.data && !canRead}
		<DeniedState
			level={2}
			title="You can't see the schedule defaults."
			description="Ask the owner of this Docker Manager for the View settings permission."
		/>
	{:else}
		<QueryView query={defaults} errorTitle="The schedule defaults could not be loaded.">
			{#snippet children(d)}
				<Card
					title="Default time zone"
					subtitle="New policies use it unless you choose another zone for them."
				>
					<div class="zone">
						<Combobox label="Time zone" options={zones} bind:value={zone} />
						{#if canEdit}
							<Button
								disabled={!zone || zone === d.timeZone}
								loading={saving && !editing}
								onclick={() =>
									patch(
										d,
										{ timeZone: zone },
										`Saved the default time zone ${zone}`
									)}>Save time zone</Button
							>
						{/if}
					</div>
				</Card>
				<Card title="Default schedules" padding="none">
					<Table
						label="Default schedules"
						rows={d.kinds}
						columns={kindColumns}
						rowKey={(k) => k.kind}
					/>
				</Card>
				{#if saveError && !editing}<Notice tone="danger" title="Not saved" live="alert"
						>{saveError}</Notice
					>{/if}
				<Dialog
					open={!!editing}
					title="Default for {editing?.label ?? ''}"
					description="New policies of this kind start with it."
					onclose={() => (editing = null)}
				>
					{#if editing}
						<CronField
							label="Schedule"
							kind={editing.kind}
							bind:cron={editCron}
							bind:timeZone={editZone}
						/>
						{#if saveError}<Notice tone="danger" title="Not saved" live="alert"
								>{saveError}</Notice
							>{/if}
						<FormFooter>
							<Button
								variant="ghost"
								onclick={() => editing && (editCron = editing.suggested)}
								>Use Docker Manager’s suggestion</Button
							>
							<Button
								variant="primary"
								loading={saving}
								onclick={() =>
									editing &&
									patch(
										d,
										{ crons: { [editing.kind]: editCron } },
										`Saved the default for ${editing.label}`
									)}>Save default</Button
							>
						</FormFooter>
					{/if}
				</Dialog>
			{/snippet}
		</QueryView>
	{/if}

	{#if can(access, 'schedule.read')}
		<Card title="Scheduled policies">
			<p class="muted">
				Every backup, update and maintenance schedule with its next runs is on the Schedules
				page.
			</p>
			<div class="link">
				<Button href={routes.schedules()} icon={CalendarClock}>Open schedules</Button>
			</div>
		</Card>
	{/if}
</Page>

<style>
	.zone {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
	}

	.zone > :global(:first-child) {
		flex: 1 1 260px;
	}

	.link {
		margin-top: var(--space-3);
	}
</style>
