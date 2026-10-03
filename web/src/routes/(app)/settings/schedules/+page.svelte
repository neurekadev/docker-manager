<script lang="ts">
	// Schedules (#13): the default time zone and the default schedule per
	// kind that new policies start with (existing policies keep theirs), in
	// words with the expression as tooltip; Docker Manager's suggestion shows
	// once, in its own column. The cross-policy view is /schedules.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
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
		describeCron,
		toast,
		type Column
	} from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { ifMatch } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
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
		title: 'Schedule Defaults',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'Schedule Defaults' }]
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
		{ id: 'cron', header: 'Default', cell: cronCell, width: '240px', stack: 'status' },
		{
			id: 'suggested',
			header: 'Docker Manager’s Suggestion',
			cell: suggestedCell,
			width: '240px',
			stack: 'meta'
		},
		{ id: 'catch', header: 'Missed Runs', cell: catchCell, width: '200px', stack: 'meta' },
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

{#snippet kindCell(k: Kind)}<span class="name">{k.label}</span>{/snippet}
{#snippet cronCell(k: Kind)}<span title={k.cron}
		>{describeCron(k.cron, defaults.data?.timeZone)}</span
	>{/snippet}
{#snippet suggestedCell(k: Kind)}
	{#if k.cron === k.suggested}<span class="muted">Same</span>{:else}<span
			class="muted"
			title={k.suggested}>{describeCron(k.suggested, defaults.data?.timeZone)}</span
		>{/if}
{/snippet}
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
		title="Schedule Defaults"
		description="New policies start with these. Existing policies keep theirs."
	/>
	{#if perms.data && !canRead}
		<DeniedState
			level={2}
			title="You can't see the schedule defaults."
			description="Ask the owner of this Docker Manager for the View Settings permission."
		/>
	{:else}
		<QueryView query={defaults} errorTitle="The schedule defaults could not be loaded.">
			{#snippet children(d)}
				<Card title="Default Time Zone">
					<div class="zone">
						<Combobox label="Time Zone" options={zones} bind:value={zone} />
						{#if canEdit}
							<Button
								disabled={!zone || zone === d.timeZone}
								loading={saving && !editing}
								onclick={() =>
									patch(
										d,
										{ timeZone: zone },
										`Saved the default time zone ${zone}`
									)}>Save Time Zone</Button
							>
						{/if}
					</div>
				</Card>
				<Card title="Default Schedules" padding="none">
					<Table
						label="Default Schedules"
						rows={d.kinds}
						columns={kindColumns}
						rowKey={(k) => k.kind}
					/>
				</Card>
				{#if saveError && !editing}<Notice tone="danger" title="Not Saved" live="alert"
						>{saveError}</Notice
					>{/if}
				<Dialog
					open={!!editing}
					title="Default for {editing?.label ?? ''}"
					onclose={() => (editing = null)}
				>
					{#if editing}
						<CronField
							label="Schedule"
							kind={editing.kind}
							bind:cron={editCron}
							bind:timeZone={editZone}
						/>
						{#if saveError}<Notice tone="danger" title="Not Saved" live="alert"
								>{saveError}</Notice
							>{/if}
						<FormFooter>
							<Button
								variant="ghost"
								onclick={() => editing && (editCron = editing.suggested)}
								>Use Docker Manager’s Suggestion</Button
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
									)}>Save Default</Button
							>
						</FormFooter>
					{/if}
				</Dialog>
			{/snippet}
		</QueryView>
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
		flex: 0 1 360px;
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}
</style>
