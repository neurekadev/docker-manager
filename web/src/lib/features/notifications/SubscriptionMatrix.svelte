<script lang="ts">
	// "What to Send" (#142) of every channel on one card: a row per kind of
	// event (grouped Hosts and Jobs; its bell switches every outcome of the
	// kind) and per outcome, a column per channel (the built-in In App
	// channel first, then by name). A filled bell sends the event through
	// the channel (In App: shows it in Notices). Changes collect in a draft
	// until "Save Changes" (one revisioned PATCH of `events` per changed
	// channel); a channel must keep at least one event. Columns of channels
	// that are off are dimmed. The first column stays in view while the
	// table scrolls sideways.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { ApiRequestError } from '$lib/api/client';
	import { Button, Card, InfoTip, errorMessage, toast } from '$lib/ui';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
	import BellToggle from './BellToggle.svelte';
	import {
		EVENT_GROUPS,
		EVENT_KINDS,
		channelColumns,
		eventsOf,
		kindBell,
		noEvents,
		outcomeBell,
		picksOf,
		toggleKind,
		toggleOutcome,
		withDraft,
		type EventKind,
		type EventOutcome,
		type EventPicks,
		type NotificationChannel
	} from './model';
	import { notificationKeys, updateChannel } from './queries';
	import { serviceLabel } from './services';

	let { channels }: { channels: NotificationChannel[] } = $props();

	const queryClient = useQueryClient();
	/** Unsaved picks by channel ID (only channels that differ). */
	let draft = $state<Record<string, EventPicks>>({});
	let busy = $state(false);

	const columns = $derived(channelColumns(channels));
	const changed = $derived(columns.filter((c) => draft[c.id]));
	const silent = $derived(changed.filter((c) => noEvents(draft[c.id])));

	const picks = (c: NotificationChannel): EventPicks => draft[c.id] ?? picksOf(c.events);
	const sub = (c: NotificationChannel) =>
		`${c.builtIn ? 'Notices' : serviceLabel(c.service)}${c.enabled ? '' : ' · Off'}`;

	function pressKind(c: NotificationChannel, kind: EventKind) {
		draft = withDraft(draft, c, toggleKind(picks(c), kind));
	}

	function pressOutcome(c: NotificationChannel, kind: EventKind, outcome: EventOutcome) {
		draft = withDraft(draft, c, toggleOutcome(picks(c), kind, outcome));
	}

	function forget(id: string) {
		draft = Object.fromEntries(Object.entries(draft).filter(([k]) => k !== id));
	}

	async function save() {
		if (silent.length || !changed.length) return;
		busy = true;
		const failed: string[] = [];
		let problem: unknown = null;
		for (const c of [...changed]) {
			try {
				await updateChannel(c, { events: eventsOf(draft[c.id]) });
				forget(c.id);
			} catch (e) {
				failed.push(c.name);
				problem = e;
			}
		}
		busy = false;
		void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
		if (!failed.length) {
			toast.success('Saved What to Send');
			return;
		}
		toast.error(`What to Send of ${failed.join(', ')} could not be saved`, {
			body:
				problem instanceof ApiRequestError && problem.status === 412
					? 'Someone changed the channel meanwhile. Discard your changes and make them again.'
					: errorMessage(problem)
		});
	}
</script>

<Card
	title="What to Send"
	subtitle="A filled bell sends the event through the channel; In App shows it in Notices."
	padding="none"
>
	<div class="scroll">
		<table class="matrix">
			<caption class="sr-only">What each notification channel sends</caption>
			<thead>
				<tr>
					<th scope="col" class="event">Event</th>
					{#each columns as c (c.id)}
						<th scope="col" class="channel" class:dim={!c.enabled}>
							<span class="name">{c.name}</span>
							<span class="sub">{sub(c)}</span>
						</th>
					{/each}
				</tr>
			</thead>
			{#each EVENT_GROUPS as g (g.group)}
				<tbody>
					<tr class="group">
						<th scope="colgroup" colspan={columns.length + 1}>{g.label}</th>
					</tr>
					{#each EVENT_KINDS.filter((k) => k.group === g.group) as k (k.kind)}
						<tr class="kind">
							<th scope="row" class="event">
								<span class="kind-label">
									<k.icon size={16} strokeWidth={1.75} aria-hidden="true" />
									{k.label}
									{#if k.hint}<InfoTip text={k.hint} />{/if}
								</span>
							</th>
							{#each columns as c (c.id)}
								<td>
									<BellToggle
										value={kindBell(k, picks(c))}
										label="{k.label} for {c.name}"
										dim={!c.enabled}
										onclick={() => pressKind(c, k.kind)}
									/>
								</td>
							{/each}
						</tr>
						{#each k.outcomes as o (o.outcome)}
							<tr class="outcome">
								<th scope="row" class="event"
									><span class="sr-only">{k.label}: </span>{o.label}</th
								>
								{#each columns as c (c.id)}
									<td>
										<BellToggle
											value={outcomeBell(picks(c), k.kind, o.outcome)}
											label="{k.label}: {o.label} for {c.name}"
											dim={!c.enabled}
											onclick={() => pressOutcome(c, k.kind, o.outcome)}
										/>
									</td>
								{/each}
							</tr>
						{/each}
					{/each}
				</tbody>
			{/each}
		</table>
	</div>
</Card>

{#if changed.length}
	<FormFooter sticky>
		{#snippet summary()}
			{#if silent.length}
				<span class="error" role="alert"
					>{silent.map((c) => c.name).join(', ')}
					{silent.length === 1 ? 'needs' : 'need'} at least one event to send.</span
				>
			{:else}
				<span
					><strong class="num">{changed.length}</strong>
					{changed.length === 1 ? 'channel' : 'channels'} changed</span
				>
			{/if}
		{/snippet}
		<Button variant="ghost" disabled={busy} onclick={() => (draft = {})}>Discard</Button>
		<Button variant="primary" loading={busy} disabled={silent.length > 0} onclick={save}
			>Save Changes</Button
		>
	</FormFooter>
{/if}

<style>
	.scroll {
		overflow-x: auto;
	}

	.matrix {
		width: 100%;
		border-collapse: collapse;
	}

	th,
	td {
		padding: 0 var(--space-2);
		border-top: 1px solid var(--border-subtle);
		text-align: center;
		vertical-align: middle;
	}

	/* The events stay in view while the channels scroll sideways. */
	.event {
		position: sticky;
		left: 0;
		z-index: 1;
		min-width: 13rem;
		padding: var(--space-1) var(--space-4);
		background: var(--surface-panel);
		text-align: left;
		font-weight: var(--weight-regular);
	}

	thead th {
		padding-top: var(--space-3);
		padding-bottom: var(--space-3);
		border-top: 0;
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
		vertical-align: bottom;
	}

	.channel {
		min-width: 6.5rem;
		max-width: 10rem;
	}

	.channel .name {
		display: block;
		overflow: hidden;
		color: var(--text-strong);
		font-size: var(--text-body);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.channel .sub {
		display: block;
	}

	.channel.dim .name {
		color: var(--text-muted);
	}

	.group th {
		position: sticky;
		left: 0;
		padding: var(--space-3) var(--space-4) var(--space-1);
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
		text-align: left;
	}

	.kind .event {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.kind-label {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
	}

	.outcome th,
	.outcome td {
		border-top-color: transparent;
	}

	.outcome .event {
		padding-left: calc(var(--space-4) + 16px + var(--space-2));
		color: var(--text-default);
	}

	.error {
		color: var(--danger);
	}

	strong {
		color: var(--text-strong);
	}
</style>
