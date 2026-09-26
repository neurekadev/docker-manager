<script lang="ts">
	// Maintenance defaults (#14): the rules new prune policies start with.
	// Changing them never changes existing policies. Editing needs
	// settings.manage; reading settings.read.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap } from '$lib/api/client';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, Card, Notice, PageHeader, toast } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { ifMatch } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import RuleList from '$lib/features/maintenance/RuleList.svelte';
	import {
		normalizeRules,
		ruleProblem,
		type MaintenanceDefaults,
		type MaintenanceRule
	} from '$lib/features/maintenance/model';
	import { maintenanceDefaultsQuery, maintenanceKeys } from '$lib/features/maintenance/queries';

	usePage({
		title: 'Maintenance defaults',
		crumbs: [{ label: 'Maintenance', href: routes.maintenance() }, { label: 'Defaults' }]
	});

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const canEdit = $derived(can(accessOf(perms.data), 'settings.manage'));
	const defaults = createQuery(() => ({
		...maintenanceDefaultsQuery(),
		refetchOnWindowFocus: false
	}));

	let rules = $state<MaintenanceRule[] | null>(null);
	let base = $state<MaintenanceDefaults | null>(null);
	let dirty = $state(false);
	let busy = $state(false);
	let error = $state<unknown>(null);

	// Load once; a live refresh while editing does not replace the edits.
	$effect(() => {
		const d = defaults.data;
		if (!d) return;
		untrack(() => {
			if (!dirty) {
				base = d;
				rules = normalizeRules(d.rules);
			}
		});
	});

	useUnsaved(
		() => 'Maintenance defaults',
		() => dirty && !busy
	);

	const problems = $derived((rules ?? []).map(ruleProblem).filter(Boolean));

	async function save() {
		if (!rules || !base) return;
		busy = true;
		error = null;
		try {
			const saved = await unwrap(
				api.PATCH('/api/v1/maintenance-defaults', {
					params: { header: { 'If-Match': ifMatch(base.revision) } },
					body: { rules }
				})
			);
			dirty = false;
			base = saved;
			rules = normalizeRules(saved.rules);
			qc.setQueryData(maintenanceKeys.defaults, saved);
			toast.success('Saved the maintenance defaults', {
				body: 'New policies start with these rules; existing policies keep theirs.'
			});
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}

	function reset() {
		if (!base) return;
		rules = normalizeRules(base.rules);
		dirty = false;
		error = null;
	}
</script>

<Page narrow>
	<PageHeader
		title="Maintenance defaults"
		description="The rules every new maintenance policy starts with. Existing policies keep their own rules."
	/>
	<QueryView
		query={defaults}
		errorTitle="The maintenance defaults could not be loaded."
		deniedTitle="You can't see the maintenance defaults."
		deniedDescription="Ask the owner of this Docker Manager for the View settings permission."
	>
		{#snippet children(d)}
			{#if error}
				<Notice tone="danger" title="The defaults were not saved" live="alert"
					>{actionError(error)}</Notice
				>
			{/if}
			<Card
				title="Default rules"
				subtitle="Docker Manager ships every rule off with a 30-day age; volume rules also need their own opt-in."
			>
				<RuleList
					rules={rules ?? []}
					info={d.categories}
					disabled={!canEdit}
					onchange={(i, nr) => {
						if (!rules) return;
						rules[i] = nr;
						dirty = true;
					}}
				/>
			</Card>
			{#if canEdit}
				<FormFooter>
					<Button variant="ghost" onclick={reset} disabled={!dirty}
						>Discard changes</Button
					>
					<Button
						variant="primary"
						loading={busy}
						disabled={!dirty || problems.length > 0}
						onclick={save}>Save defaults</Button
					>
				</FormFooter>
			{/if}
		{/snippet}
	</QueryView>
</Page>
