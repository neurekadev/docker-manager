// Notification channels (#142) in words: the kinds of events and their
// outcomes a channel can send ("What to Send"), what a channel sends, its
// status and why it fails. Pure (services.spec.ts).
import Archive from '@lucide/svelte/icons/archive';
import ArchiveRestore from '@lucide/svelte/icons/archive-restore';
import ChartPie from '@lucide/svelte/icons/chart-pie';
import CircleX from '@lucide/svelte/icons/circle-x';
import MemoryStick from '@lucide/svelte/icons/memory-stick';
import PackageCheck from '@lucide/svelte/icons/package-check';
import ServerOff from '@lucide/svelte/icons/server-off';
import Thermometer from '@lucide/svelte/icons/thermometer';
import Wrench from '@lucide/svelte/icons/wrench';
import type { Schema } from '$lib/api/client';
import type { IconComponent } from '$lib/design/icons';
import { resourceIcon } from '$lib/features/common/resourceIcons';

export type NotificationChannel = Schema<'NotificationChannel'>;
export type NotificationTest = Schema<'NotificationChannelTest'>;
/** The outcomes a channel sends of one kind of event. */
export type EventSubscription = Schema<'NotificationSubscription'>;
export type EventKind = EventSubscription['kind'];
export type EventOutcome = EventSubscription['outcomes'][number];
/** The groups of "What to Send": problems of the hosts, runs of jobs. */
export type EventGroup = 'hosts' | 'jobs';

export interface EventKindInfo {
	kind: EventKind;
	/** The checkbox label ("Backups"). */
	label: string;
	/** In a sentence ("backups"). */
	short: string;
	/** What else it covers, when the label does not say it (the row's (i)). */
	hint?: string;
	group: EventGroup;
	icon: IconComponent;
	/** What a channel can send of it, in display order, with the checkbox labels. */
	outcomes: { outcome: EventOutcome; label: string }[];
}

const PROBLEM: EventKindInfo['outcomes'] = [
	{ outcome: 'warning', label: 'Warning' },
	{ outcome: 'critical', label: 'Critical' },
	{ outcome: 'resolved', label: 'Resolved' }
];

/** The kinds of events in display order (the manager's), with their outcomes. */
export const EVENT_KINDS: EventKindInfo[] = [
	{
		kind: 'disk_health',
		label: 'Disk Health',
		short: 'disk health',
		group: 'hosts',
		icon: resourceIcon('disk').icon,
		outcomes: PROBLEM
	},
	{
		kind: 'raid',
		label: 'RAID',
		short: 'RAID',
		group: 'hosts',
		icon: resourceIcon('raidArray').icon,
		outcomes: PROBLEM
	},
	{
		kind: 'temperature',
		label: 'Temperature',
		short: 'temperature',
		group: 'hosts',
		icon: Thermometer,
		outcomes: PROBLEM
	},
	{
		kind: 'disk_space',
		label: 'Disk Space',
		short: 'disk space',
		group: 'hosts',
		icon: ChartPie,
		outcomes: PROBLEM
	},
	{
		kind: 'memory',
		label: 'Memory',
		short: 'memory',
		group: 'hosts',
		icon: MemoryStick,
		outcomes: PROBLEM
	},
	{
		kind: 'environment_offline',
		label: 'Environment Offline',
		short: 'offline environments',
		group: 'hosts',
		icon: ServerOff,
		outcomes: [
			{ outcome: 'critical', label: 'Offline' },
			{ outcome: 'resolved', label: 'Back Online' }
		]
	},
	{
		kind: 'backup',
		label: 'Backups',
		short: 'backups',
		hint: 'Finished backups, and failed verifications, retention and imports started by a schedule or an API token.',
		group: 'jobs',
		icon: Archive,
		outcomes: [
			{ outcome: 'failure', label: 'Failure' },
			{ outcome: 'warning', label: 'Warning' },
			{ outcome: 'success', label: 'Success' }
		]
	},
	{
		kind: 'restore',
		label: 'Restores',
		short: 'restores',
		group: 'jobs',
		icon: ArchiveRestore,
		outcomes: [
			{ outcome: 'failure', label: 'Failure' },
			{ outcome: 'success', label: 'Success' }
		]
	},
	{
		kind: 'prune',
		label: 'Prune',
		short: 'prune',
		group: 'jobs',
		icon: Wrench,
		outcomes: [
			{ outcome: 'failure', label: 'Failure' },
			{ outcome: 'success', label: 'Success' }
		]
	},
	{
		kind: 'updates',
		label: 'Image Updates',
		short: 'image updates',
		hint: 'Updates a check found, finished update runs, and failed update checks started by a schedule or an API token.',
		group: 'jobs',
		icon: PackageCheck,
		outcomes: [
			{ outcome: 'available', label: 'Available' },
			{ outcome: 'failure', label: 'Failure' },
			{ outcome: 'success', label: 'Applied' }
		]
	},
	{
		kind: 'job_failed',
		label: 'Other Jobs',
		short: 'other jobs',
		hint: 'Failed jobs that an API token started and no row above covers: deploys, starts and stops, image pulls and builds, container, volume and file actions, and migrations.',
		group: 'jobs',
		icon: CircleX,
		outcomes: [
			{ outcome: 'failure', label: 'Failure' },
			{ outcome: 'warning', label: 'Warning' },
			{ outcome: 'resolved', label: 'Resolved' }
		]
	}
];

/** The groups of "What to Send" in order, with their legends. */
export const EVENT_GROUPS: { group: EventGroup; label: string }[] = [
	{ group: 'hosts', label: 'Hosts' },
	{ group: 'jobs', label: 'Jobs' }
];

export const ALL_KINDS: EventKind[] = EVENT_KINDS.map((k) => k.kind);

/** A kind's labels, outcomes and icon (undefined for a kind this UI does not know). */
export function eventKind(kind: string): EventKindInfo | undefined {
	return EVENT_KINDS.find((k) => k.kind === kind);
}

/** The chosen outcomes by kind (the dialog's state). */
export type EventPicks = Partial<Record<EventKind, readonly EventOutcome[]>>;

/** Every outcome of every kind: a new channel's default. */
export function allEvents(): EventSubscription[] {
	return EVENT_KINDS.map((k) => ({ kind: k.kind, outcomes: k.outcomes.map((o) => o.outcome) }));
}

/** The picks of a channel's events (unknown kinds and outcomes dropped). */
export function picksOf(events: readonly EventSubscription[]): EventPicks {
	const out: EventPicks = {};
	for (const e of events) {
		const k = eventKind(e.kind);
		if (!k) continue;
		out[k.kind] = k.outcomes.map((o) => o.outcome).filter((o) => e.outcomes.includes(o));
	}
	return out;
}

/**
 * The events to send from the picks: kinds in display order, each kind's
 * outcomes in its order, kinds without outcomes left out.
 */
export function eventsOf(picks: EventPicks): EventSubscription[] {
	const out: EventSubscription[] = [];
	for (const k of EVENT_KINDS) {
		const chosen = picks[k.kind] ?? [];
		const outcomes = k.outcomes.map((o) => o.outcome).filter((o) => chosen.includes(o));
		if (outcomes.length) out.push({ kind: k.kind, outcomes });
	}
	return out;
}

/** Whether every, some or none of a kind's outcomes are picked (its master checkbox). */
export function kindState(k: EventKindInfo, picks: EventPicks): 'all' | 'some' | 'none' {
	const chosen = picks[k.kind] ?? [];
	const n = k.outcomes.filter((o) => chosen.includes(o.outcome)).length;
	if (n === 0) return 'none';
	return n === k.outcomes.length ? 'all' : 'some';
}

/** The picks with every outcome of a kind on or off (its master checkbox). */
export function pickKind(picks: EventPicks, kind: EventKind, on: boolean): EventPicks {
	const k = eventKind(kind);
	const out: EventPicks = { ...picks };
	out[kind] = on && k ? k.outcomes.map((o) => o.outcome) : [];
	return out;
}

/** The picks with one outcome of a kind on or off. */
export function pickOutcome(
	picks: EventPicks,
	kind: EventKind,
	outcome: EventOutcome,
	on: boolean
): EventPicks {
	const had = (picks[kind] ?? []).filter((o) => o !== outcome);
	const out: EventPicks = { ...picks };
	out[kind] = on ? [...had, outcome] : had;
	return out;
}

/** Whether nothing at all is picked (the dialog refuses to save). */
export function noEvents(picks: EventPicks): boolean {
	return eventsOf(picks).length === 0;
}

function capitalize(s: string): string {
	return s ? s[0].toUpperCase() + s.slice(1) : s;
}

/** Whether every outcome of each sent kind is sent. */
function everyOutcome(events: readonly EventSubscription[]): boolean {
	return events.every((e) => {
		const k = eventKind(e.kind);
		return !k || k.outcomes.every((o) => e.outcomes.includes(o.outcome));
	});
}

/**
 * What a channel sends in a few words: "All events", "Backups and
 * restores", "Disk health", "4 kinds of events", with " (some
 * outcomes)" when not every outcome of the chosen kinds is sent.
 */
export function kindsSummary(events: readonly EventSubscription[]): string {
	const sent = events.filter((e) => e.outcomes.length > 0 && eventKind(e.kind));
	const known = EVENT_KINDS.filter((k) => sent.some((e) => e.kind === k.kind));
	if (known.length === 0) return 'Nothing';
	const partial = !everyOutcome(sent);
	let base: string;
	if (known.length === EVENT_KINDS.length) base = partial ? 'Every kind of event' : 'All events';
	else if (known.length === 1) base = capitalize(known[0].short);
	else if (known.length === 2) base = capitalize(`${known[0].short} and ${known[1].short}`);
	else base = `${known.length} kinds of events`;
	return partial ? `${base} (some outcomes)` : base;
}

/**
 * Every sent kind with its outcomes, one per line (the Sends column's
 * tooltip): "Backups: Failure, Success".
 */
export function eventsDetail(events: readonly EventSubscription[]): string {
	return EVENT_KINDS.flatMap((k) => {
		const e = events.find((x) => x.kind === k.kind);
		const labels = k.outcomes
			.filter((o) => e?.outcomes.includes(o.outcome))
			.map((o) => o.label);
		return labels.length ? [`${k.label}: ${labels.join(', ')}`] : [];
	}).join('\n');
}

/** Where a channel's events come from: every environment, one by name, or a count. */
export function environmentsSummary(
	all: boolean,
	ids: readonly string[],
	nameOf: (id: string) => string | undefined
): string {
	if (all) return 'every environment';
	// A filter whose environments are all gone sends no environment's events.
	if (ids.length === 0) return 'no environment';
	if (ids.length === 1) return nameOf(ids[0]) ?? '1 environment';
	return `${ids.length} environments`;
}

/** The "Sends" column: "All events, every environment". */
export function sendsSummary(
	c: Pick<NotificationChannel, 'events' | 'allEnvironments' | 'environmentIds'>,
	nameOf: (id: string) => string | undefined
): string {
	return `${kindsSummary(c.events)}, ${environmentsSummary(c.allEnvironments, c.environmentIds, nameOf)}`;
}

/** What went wrong in a send, and what to check (the manager's classes). */
export const ERROR_TEXT: Record<string, string> = {
	dns: 'The server name in the address could not be found. Check the address.',
	connect:
		'Docker Manager could not connect to the service. Check the host and port and that the service is reachable from Docker Manager.',
	tls: "The secure connection failed. Check the server's certificate and the encryption setting.",
	timeout: 'The service did not answer in time. Check that it is reachable from Docker Manager.',
	auth: 'The service refused the credentials. Check the token, password or webhook address.',
	http_4xx:
		'The service rejected the message. Check the address and the target (channel, topic, chat or recipient).',
	http_5xx: 'The service had a problem on its side. Try again later.',
	redirect: 'The service redirected to another address. Enter the final address instead.',
	rejected: "The service did not accept the message. Check the channel's settings.",
	invalid_url: "The address can't be used for this service. Check it and save it again."
};

export function errorText(errorClass: string | undefined): string {
	return (
		(errorClass && ERROR_TEXT[errorClass]) ||
		"The message could not be sent. Check the channel's settings."
	);
}

export interface ChannelStatus {
	/** The StatusBadge state (its tone). */
	status: 'healthy' | 'failed' | 'unknown' | 'stopped';
	label: 'Working' | 'Failing' | 'Not Tested' | 'Off';
	/** Why it fails, in words (the badge's tooltip). */
	reason?: string;
}

/** A channel's status: Off, Not Tested, Working or Failing (with the reason). */
export function channelStatus(
	c: Pick<NotificationChannel, 'enabled' | 'lastResult'>
): ChannelStatus {
	if (!c.enabled) return { status: 'stopped', label: 'Off' };
	if (!c.lastResult) return { status: 'unknown', label: 'Not Tested' };
	if (c.lastResult === 'ok') return { status: 'healthy', label: 'Working' };
	return { status: 'failed', label: 'Failing', reason: errorText(c.lastResult) };
}

/** The toast of a test message. */
export function testOutcome(
	name: string,
	t: Pick<NotificationTest, 'ok' | 'errorClass' | 'message'>
): { ok: boolean; title: string; body?: string } {
	if (t.ok) return { ok: true, title: `Test message sent to ${name}` };
	return {
		ok: false,
		title: `The test message to ${name} failed`,
		body: t.message || errorText(t.errorClass)
	};
}
