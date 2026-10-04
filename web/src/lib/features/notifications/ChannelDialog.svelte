<script lang="ts">
	// Add or edit a notification channel (#142). The destination is entered
	// as the service's own fields (a Discord or Slack webhook URL pasted as
	// it is, an SMTP server and login, ...) and stored as one Shoutrrr URL,
	// sealed on the manager. Editing shows it masked until "Show address"
	// reads it back (owner, confirming their identity); choosing another
	// service replaces it. Below it the environments the channel is for and
	// Enabled. What it sends is chosen on the page's "What to Send" bells
	// (SubscriptionMatrix; a new channel sends everything). The built-in In
	// App channel has no name or address to edit: only its environments and
	// Enabled.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Bell from '@lucide/svelte/icons/bell';
	import Eye from '@lucide/svelte/icons/eye';
	import LockKeyhole from '@lucide/svelte/icons/lock-keyhole';
	import { ApiRequestError } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import {
		Button,
		Checkbox,
		Dialog,
		PasswordField,
		Select,
		Switch,
		TextField,
		errorMessage,
		fieldError,
		toast
	} from '$lib/ui';
	import { runTest } from './actions';
	import type { NotificationChannel } from './model';
	import { createChannel, notificationKeys, revealAddress, updateChannel } from './queries';
	import { SERVICE_ICONS } from './serviceIcons';
	import {
		SERVICES,
		buildUrl,
		initialValues,
		parseUrl,
		serviceIdOf,
		serviceSpec,
		type ServiceId,
		type Values
	} from './services';

	interface Props {
		open?: boolean;
		/** The channel to edit; null adds one. */
		channel?: NotificationChannel | null;
	}

	let { open = $bindable(false), channel = null }: Props = $props();
	const queryClient = useQueryClient();
	const envs = createQuery(() => ({ ...environmentsQuery(), enabled: open }));
	const activeEnvs = $derived((envs.data ?? []).filter((e) => !e.archivedAt));

	let name = $state('');
	let service = $state<ServiceId | ''>('');
	let values = $state<Values>({});
	let enabled = $state(true);
	/** "All environments" is an explicit choice, never an empty selection. */
	let envMode = $state<'all' | 'some'>('all');
	let envIds = $state<string[]>([]);

	/** Edit mode: the stored address was read back (and its URL). */
	let revealed = $state<string | null>(null);
	/** Edit mode: another service was chosen, so a new address is entered. */
	let replacing = $state(false);
	let revealing = $state(false);
	let revealError = $state<string | null>(null);
	let busy = $state(false);
	let submitted = $state(false);
	let failure = $state<unknown>(null);

	$effect(() => {
		if (!open) return;
		untrack(() => {
			const c = channel;
			name = c?.name ?? '';
			service = c ? serviceIdOf(c.service) : '';
			values = {};
			enabled = c?.enabled ?? true;
			envIds = c ? [...c.environmentIds] : [];
			envMode = !c || c.allEnvironments ? 'all' : 'some';
			revealed = null;
			replacing = false;
			revealing = false;
			revealError = null;
			busy = false;
			submitted = false;
			failure = null;
		});
	});

	const editing = $derived(!!channel);
	/** The In App channel: the bell, without an address. */
	const builtIn = $derived(!!channel?.builtIn);
	/** The environment picker: only with a choice (or a filter to keep). */
	const showEnvs = $derived(activeEnvs.length > 1 || envMode === 'some');
	/**
	 * The environments to tick: the active ones, then every one the channel
	 * lists or listed that is archived or gone, so a filter is never dropped
	 * without the owner seeing it (the manager keeps them; it never widens a
	 * filter to every environment).
	 */
	const envChoices = $derived.by(() => {
		const out = activeEnvs.map((e) => ({ id: e.id, label: e.name }));
		const listed = [...(channel?.environmentIds ?? []), ...envIds];
		for (const id of listed) {
			if (out.some((c) => c.id === id)) continue;
			const known = envs.data?.find((e) => e.id === id);
			out.push({ id, label: known ? `${known.name} (archived)` : 'Removed Environment' });
		}
		return out;
	});
	/** The address fields are shown (adding, shown again, or replaced). */
	const fieldsShown = $derived(!!service && (!editing || revealed !== null || replacing));
	const spec = $derived(service ? serviceSpec(service) : null);
	const problems = $derived(spec && fieldsShown ? spec.check(values) : {});
	const url = $derived(service && fieldsShown ? buildUrl(service, values) : null);
	/** The address to send: always when adding, when changed when editing. */
	const newAddress = $derived(fieldsShown && url !== null && url !== revealed ? url : undefined);

	const serviceOptions = SERVICES.map((s) => ({
		value: s.id,
		label: s.label,
		icon: SERVICE_ICONS[s.id]
	}));

	const nameError = $derived.by(() => {
		if (submitted && !name.trim()) return 'Enter a name.';
		if (
			failure instanceof ApiRequestError &&
			failure.apiError?.code === 'notification_channel_name_taken'
		)
			return 'Another channel already uses this name.';
		return fieldError(failure, 'body.name') ?? null;
	});
	const envError = $derived(
		submitted && envMode === 'some' && envIds.length === 0
			? 'Choose at least one environment.'
			: (fieldError(failure, 'body.environmentIds') ?? null)
	);
	const addressError = $derived(fieldError(failure, 'body.address') ?? null);
	const serviceError = $derived(submitted && !service ? 'Choose a service.' : null);

	const ready = $derived(
		(builtIn || (!!name.trim() && !!service)) &&
			(envMode === 'all' || envIds.length > 0) &&
			(!fieldsShown || Object.keys(problems).length === 0)
	);

	function chooseService(next: string) {
		const id = next as ServiceId;
		if (editing && revealed === null) replacing = id !== serviceIdOf(channel!.service);
		if (editing && revealed !== null) {
			// Switching back to the stored address's service shows it again.
			const stored = parseUrl(revealed);
			values = stored.service === id ? stored.values : initialValues(id);
		} else {
			values = initialValues(id);
		}
		service = id;
	}

	async function showAddress() {
		if (!channel) return;
		revealing = true;
		revealError = null;
		try {
			const address = await withStepUp(() => revealAddress(channel!.id));
			const parsed = parseUrl(address);
			revealed = address;
			replacing = false;
			service = parsed.service;
			values = parsed.values;
		} catch (e) {
			revealError = errorMessage(e);
		} finally {
			revealing = false;
		}
	}

	function toggleEnv(id: string, on: boolean) {
		envIds = on ? [...envIds.filter((e) => e !== id), id] : envIds.filter((e) => e !== id);
	}

	async function save() {
		submitted = true;
		failure = null;
		if (!ready || (!builtIn && ((!editing && !url) || (editing && replacing && !url)))) return;
		busy = true;
		// Sent as chosen: every environment explicitly, or the ticked ones
		// (archived ones included; the manager keeps them).
		const allEnvironments = envMode === 'all';
		const environmentIds = allEnvironments ? [] : envIds;
		try {
			if (!channel) {
				const created = await withStepUp(() =>
					createChannel({
						name: name.trim(),
						address: url!,
						enabled,
						allEnvironments,
						environmentIds
					})
				);
				toast.success(`Added ${created.name}`, {
					body: 'It sends every event. Choose what under What to Send, and send a test message to check that it arrives.',
					action: {
						label: 'Send Test',
						onclick: () => void runTest(created, queryClient)
					}
				});
			} else {
				// The In App channel keeps its name and has no address.
				const body = builtIn
					? { enabled, allEnvironments, environmentIds }
					: {
							name: name.trim(),
							enabled,
							allEnvironments,
							environmentIds,
							address: newAddress
						};
				// A new address needs a recent step-up; the manager asks for it.
				const saved = await withStepUp(() => updateChannel(channel!, body));
				toast.success(`Saved ${saved.name}`);
			}
			void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
			open = false;
		} catch (e) {
			failure = e;
		} finally {
			busy = false;
		}
	}

	const generalError = $derived.by(() => {
		if (!failure || nameError || envError || addressError) return null;
		if (failure instanceof ApiRequestError && failure.status === 412)
			return 'Someone changed this channel meanwhile. Close the dialog and open it again.';
		return errorMessage(failure);
	});
</script>

<Dialog
	bind:open
	title={channel ? `Edit ${channel.name}` : 'Add Notification Channel'}
	size="lg"
	dismissible={!busy}
>
	<form
		id="channel-form"
		class="form"
		novalidate
		onsubmit={(e) => {
			e.preventDefault();
			void save();
		}}
	>
		<section class="col" aria-labelledby="channel-destination">
			<h3 id="channel-destination" class="subsection-title">Destination</h3>
			{#if builtIn}
				<div class="stored">
					<Bell size={16} strokeWidth={1.75} aria-hidden="true" />
					<div class="stored-text">
						<p class="stored-title">Notices in Docker Manager</p>
						<p class="muted">
							Shows what you choose in the Notices bell of everyone who may see it.
							Built in: it can't be deleted.
						</p>
					</div>
				</div>
			{:else}
				<TextField
					label="Name"
					required
					bind:value={name}
					placeholder="Ops on Discord"
					maxlength={100}
					error={nameError}
				/>
				<Select
					label="Service"
					required
					options={serviceOptions}
					value={service}
					placeholder="Choose a service"
					onchange={chooseService}
					description={spec?.hint}
					error={serviceError}
				/>
				{#if spec && fieldsShown}
					{#each spec.fields as f (f.key)}
						{#if f.kind === 'select'}
							<Select
								label={f.label}
								options={f.options ?? []}
								bind:value={() => values[f.key] ?? '', (v) => (values[f.key] = v)}
								description={f.description}
								info={f.info}
								optional={f.optional}
							/>
						{:else if f.kind === 'secret'}
							<PasswordField
								label={f.label}
								required={f.required}
								revealed={revealed !== null}
								autocomplete="off"
								placeholder={f.placeholder}
								description={f.description}
								info={f.info}
								optional={f.optional}
								bind:value={() => values[f.key] ?? '', (v) => (values[f.key] = v)}
								error={submitted ? problems[f.key] : undefined}
							/>
						{:else}
							<TextField
								label={f.label}
								required={f.required}
								mono={f.mono}
								autocomplete="off"
								spellcheck="false"
								inputmode={f.inputmode}
								placeholder={f.placeholder}
								description={f.description}
								info={f.info}
								optional={f.optional}
								bind:value={() => values[f.key] ?? '', (v) => (values[f.key] = v)}
								error={submitted ? problems[f.key] : undefined}
							/>
						{/if}
					{/each}
					{#if addressError}<p class="error" role="alert">{addressError}</p>{/if}
				{:else if channel && service}
					<div class="stored">
						<LockKeyhole size={16} strokeWidth={1.75} aria-hidden="true" />
						<div class="stored-text">
							<p class="stored-title">The address is stored encrypted</p>
							<p class="muted">
								Show it to view or change it. Choosing another service replaces it.
							</p>
							<p class="mask mono" aria-hidden="true">••••••••••••••••</p>
						</div>
						<Button
							size="sm"
							icon={Eye}
							loading={revealing}
							onclick={() => void showAddress()}>Show Address</Button
						>
					</div>
					{#if revealError}<p class="error" role="alert">{revealError}</p>{/if}
				{/if}
			{/if}
			{#if showEnvs}
				<Select
					label="Environments"
					options={[
						{ value: 'all', label: 'All Environments' },
						{ value: 'some', label: 'Some Environments' }
					]}
					bind:value={() => envMode, (v) => (envMode = v as 'all' | 'some')}
					description={envMode === 'all'
						? 'Includes environments you add later.'
						: undefined}
				/>
				{#if envMode === 'some'}
					<fieldset
						class="group"
						aria-describedby={envError ? 'channel-envs-error' : undefined}
					>
						<legend class="sr-only">Environments to Send About</legend>
						{#each envChoices as e (e.id)}
							<Checkbox
								label={e.label}
								checked={envIds.includes(e.id)}
								onchange={(ev) => toggleEnv(e.id, ev.currentTarget.checked)}
							/>
						{/each}
						{#if envError}<p id="channel-envs-error" class="field-error">
								{envError}
							</p>{/if}
					</fieldset>
				{/if}
			{/if}
			<div class="enabled">
				<Switch
					label="Enabled"
					description={enabled
						? undefined
						: builtIn
							? 'Notices then show only the jobs you start.'
							: 'You can still send a test.'}
					bind:checked={enabled}
				/>
			</div>
		</section>

		{#if generalError}<p class="error" role="alert">{generalError}</p>{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button type="submit" form="channel-form" variant="primary" loading={busy}
			>{channel ? 'Save Changes' : 'Add Channel'}</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.col {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
	}

	.group {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
		margin: 0;
		padding: 0;
		border: 0;
	}

	.stored {
		display: flex;
		align-items: flex-start;
		gap: var(--space-3);
		padding: var(--space-3);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		color: var(--text-muted);
	}

	.stored-text {
		display: flex;
		flex: 1;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.stored-title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.muted {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.mask {
		margin-top: var(--space-1);
		color: var(--text-muted);
		letter-spacing: 0.1em;
	}

	.enabled {
		padding-top: var(--space-3);
		border-top: 1px solid var(--border-subtle);
	}

	.field-error {
		color: var(--danger);
		font-size: var(--text-caption);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}

	@media (max-width: 767px) {
		.stored {
			flex-wrap: wrap;
		}
	}
</style>
