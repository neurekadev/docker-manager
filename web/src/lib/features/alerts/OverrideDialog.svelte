<script lang="ts">
	// Add or edit an environment's override of the alert thresholds: the
	// environment (only active ones without an override; fixed while
	// editing) and the six levels, each optional (empty: the default, shown
	// as placeholder; 0 turns it off there). Saving replaces the whole
	// settings (the saved defaults and every override) with If-Match.
	import { untrack } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import { ApiRequestError } from '$lib/api/client';
	import { actionError } from '$lib/features/common/errors';
	import { Button, Dialog, Select, fieldError, toast } from '$lib/ui';
	import { alertSettingsKey, saveAlertSettings } from './queries';
	import ThresholdGrid from './ThresholdGrid.svelte';
	import {
		THRESHOLD_KEYS,
		checkThresholds,
		noErrors,
		overrideForm,
		overrideOf,
		serverThresholdErrors,
		settingsBody,
		type AlertSettings,
		type ThresholdForm,
		type ThresholdKey
	} from './thresholds';

	interface Props {
		open?: boolean;
		settings: AlertSettings;
		/** The override to edit (its environment); null adds one. */
		environmentId?: string | null;
		/** Environments by ID: the names, and which are active. */
		environments: readonly { id: string; name: string; archivedAt?: string }[];
	}

	let { open = $bindable(false), settings, environmentId = null, environments }: Props = $props();
	const queryClient = useQueryClient();

	let envId = $state('');
	let form = $state<ThresholdForm>(overrideForm(undefined));
	let submitted = $state(false);
	let busy = $state(false);
	let failure = $state<unknown>(null);

	$effect(() => {
		if (!open) return;
		untrack(() => {
			envId = environmentId ?? '';
			form = overrideForm(settings.overrides.find((o) => o.environmentId === environmentId));
			submitted = false;
			busy = false;
			failure = null;
		});
	});

	const editing = $derived(!!environmentId);
	const nameOf = (id: string) =>
		environments.find((e) => e.id === id)?.name ?? 'Removed Environment';
	const options = $derived(
		editing
			? [{ value: environmentId!, label: nameOf(environmentId!) }]
			: environments
					.filter(
						(e) =>
							!e.archivedAt &&
							!settings.overrides.some((o) => o.environmentId === e.id)
					)
					.map((e) => ({ value: e.id, label: e.name }))
	);
	const placeholders = $derived(
		Object.fromEntries(
			THRESHOLD_KEYS.map((k) => [
				k,
				settings.thresholds[k] === 0 ? 'Off' : String(settings.thresholds[k])
			])
		) as Record<ThresholdKey, string>
	);

	/** Where this override sits in the request (the server's field errors name it). */
	const index = $derived.by(() => {
		const i = settings.overrides.findIndex((o) => o.environmentId === envId);
		return i < 0 ? settings.overrides.length : i;
	});
	const errors = $derived({
		...checkThresholds(form, settings.thresholds),
		...serverThresholdErrors(failure, index)
	});
	const envError = $derived(
		submitted && !envId
			? 'Choose an environment.'
			: (fieldError(failure, `body.overrides[${index}].environmentId`) ?? null)
	);
	const generalError = $derived.by(() => {
		if (!failure || envError || !noErrors(serverThresholdErrors(failure, index))) return null;
		return actionError(failure, {
			precondition_failed:
				'Someone changed the alert thresholds meanwhile. Close the dialog, check them and try again.'
		});
	});

	async function save() {
		submitted = true;
		failure = null;
		if (!envId || !noErrors(checkThresholds(form, settings.thresholds))) return;
		busy = true;
		const next = overrideOf(envId, form);
		const overrides = editing
			? settings.overrides.map((o) => (o.environmentId === envId ? next : o))
			: [...settings.overrides, next];
		const name = nameOf(envId);
		try {
			const saved = await saveAlertSettings(
				settings,
				settingsBody(settings.thresholds, overrides)
			);
			queryClient.setQueryData(alertSettingsKey, saved);
			toast.success(
				editing ? `Saved the override of ${name}` : `Added an override for ${name}`
			);
			open = false;
		} catch (e) {
			failure = e;
			if (e instanceof ApiRequestError && e.status === 412)
				void queryClient.invalidateQueries({ queryKey: alertSettingsKey });
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title="Override Thresholds"
	description="Empty uses the default; 0 turns a level off."
	size="md"
	dismissible={!busy}
>
	<form
		id="override-form"
		class="form"
		novalidate
		onsubmit={(e) => {
			e.preventDefault();
			void save();
		}}
	>
		<Select
			label="Environment"
			required
			{options}
			value={envId}
			disabled={editing}
			placeholder={options.length ? 'Choose an environment' : 'Every environment has one'}
			onchange={(v) => (envId = v)}
			error={envError}
		/>
		<ThresholdGrid
			label="Levels of the Environment"
			{form}
			{errors}
			{placeholders}
			optional
			onchange={(k, v) => {
				form = { ...form, [k]: v };
				failure = null;
			}}
		/>
		{#if generalError}<p class="error" role="alert">{generalError}</p>{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button type="submit" form="override-form" variant="primary" loading={busy}
			>{editing ? 'Save Changes' : 'Add Override'}</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
