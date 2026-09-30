// Notification channels (#142) in words: what a channel sends, its status
// and why it fails. Pure (services.spec.ts).
import type { Schema } from '$lib/api/client';

export type NotificationChannel = Schema<'NotificationChannel'>;
export type NotificationTest = Schema<'NotificationChannelTest'>;
export type EventKind = NotificationChannel['eventKinds'][number];

/** The event kinds in display order, with their checkbox labels. */
export const EVENT_KINDS: { kind: EventKind; label: string; short: string }[] = [
	{ kind: 'disk_health', label: 'Disk health problems', short: 'disk health' },
	{ kind: 'raid', label: 'RAID problems', short: 'RAID' },
	{ kind: 'environment_offline', label: 'Environment offline', short: 'offline environments' },
	{ kind: 'job_failed', label: 'Failed jobs', short: 'failed jobs' },
	{ kind: 'updates_available', label: 'Updates available', short: 'updates' }
];

export const ALL_KINDS: EventKind[] = EVENT_KINDS.map((k) => k.kind);

function capitalize(s: string): string {
	return s ? s[0].toUpperCase() + s.slice(1) : s;
}

/** The subscribed kinds in a few words: "All events", "Failed jobs and updates", "3 kinds of events". */
export function kindsSummary(kinds: readonly string[]): string {
	const known = EVENT_KINDS.filter((k) => kinds.includes(k.kind));
	if (known.length === EVENT_KINDS.length) return 'All events';
	if (known.length === 0) return 'Nothing';
	if (known.length === 1) return capitalize(known[0].short);
	if (known.length === 2) return capitalize(`${known[0].short} and ${known[1].short}`);
	return `${known.length} kinds of events`;
}

/** Where a channel's events come from: every environment, one by name, or a count. */
export function environmentsSummary(
	ids: readonly string[],
	nameOf: (id: string) => string | undefined
): string {
	if (ids.length === 0) return 'every environment';
	if (ids.length === 1) return nameOf(ids[0]) ?? '1 environment';
	return `${ids.length} environments`;
}

/** The "Sends" column: "All events, every environment". */
export function sendsSummary(
	c: Pick<NotificationChannel, 'eventKinds' | 'environmentIds'>,
	nameOf: (id: string) => string | undefined
): string {
	return `${kindsSummary(c.eventKinds)}, ${environmentsSummary(c.environmentIds, nameOf)}`;
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
	label: 'Working' | 'Failing' | 'Not tested' | 'Off';
	/** Why it fails, in words (the badge's tooltip). */
	reason?: string;
}

/** A channel's status: Off, Not tested, Working or Failing (with the reason). */
export function channelStatus(
	c: Pick<NotificationChannel, 'enabled' | 'lastResult'>
): ChannelStatus {
	if (!c.enabled) return { status: 'stopped', label: 'Off' };
	if (!c.lastResult) return { status: 'unknown', label: 'Not tested' };
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
