// Schedule defaults (#13) for new policies: the editable instance defaults
// when the caller may read settings, otherwise DockYard's shipped
// suggestions. The server applies the same defaults when a field is
// omitted; the forms show them so the user sees what will be saved.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';

export type ScheduleDefaults = Schema<'ScheduleDefaults'>;

/** DockYard's shipped suggestions (#13), used when defaults are not readable. */
export const SUGGESTED: Record<string, string> = {
	backup: '0 2 * * *',
	update_check: '0 3 * * *',
	update_run: '0 4 * * *',
	prune: '0 3 * * 0',
	backup_verification: '0 5 * * 0'
};

export const scheduleDefaultsKey = liveKeys.item('policies', 'schedule-defaults');

export function scheduleDefaultsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: scheduleDefaultsKey,
		queryFn: ({ signal }): Promise<ScheduleDefaults> =>
			unwrap(client.GET('/api/v1/schedule-defaults', { signal })),
		staleTime: 60_000,
		retry: false
	});
}

/** The browser's IANA zone (fallback UTC). */
export function browserTimeZone(): string {
	try {
		return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
	} catch {
		return 'UTC';
	}
}

/** Default cron and zone of a schedule kind for a new policy. */
export function defaultSchedule(
	kind: string,
	defaults: ScheduleDefaults | undefined
): { cron: string; timeZone: string } {
	const k = defaults?.kinds.find((x) => x.kind === kind);
	return {
		cron: k?.cron ?? SUGGESTED[kind] ?? '0 3 * * *',
		timeZone: defaults?.timeZone ?? browserTimeZone()
	};
}
