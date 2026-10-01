// Search and filters of the Notifications tab (ListCard, like the other
// lists): the kind (backups and restores, prune, image updates), the
// outcome (Done, Warning, Failed) and, while every environment is shown,
// the environment. The server applies them (GET /notifications) and the
// same filters check the loaded rows; the search runs over the title, the
// detail, the environment, the kind, the outcome and the labelled values.
// Pure; tested in filters.spec.ts.
import {
	activeValue,
	environmentFilter,
	type FilterContext,
	type ListFilter,
	type ListFilterState
} from '$lib/features/resources/filters';
import {
	NOTIFICATION_KINDS,
	NOTIFICATION_OUTCOMES,
	notificationKindLabel,
	outcomeLabel,
	type Notification,
	type NotificationKind,
	type NotificationOutcome
} from './model';
import type { NotificationFilter } from './queries';

/** The list's name in the stored filters (ListFilters). */
export const NOTIFICATIONS_LIST = 'notifications';

export function notificationFilters(ctx: FilterContext): ListFilter<Notification>[] {
	const filters: ListFilter<Notification>[] = [
		{
			id: 'kind',
			label: 'Kind',
			all: 'All kinds',
			options: NOTIFICATION_KINDS.map((k) => ({ value: k.kind, label: k.label })),
			match: (n, v) => n.kind === v
		},
		{
			id: 'outcome',
			label: 'Outcome',
			all: 'All outcomes',
			options: NOTIFICATION_OUTCOMES.map((o) => ({ value: o.outcome, label: o.label })),
			match: (n, v) => n.outcome === v
		}
	];
	if (ctx.envs.length) filters.push(environmentFilter<Notification>(ctx.envs));
	return filters;
}

/**
 * The server-side filters of GET /notifications from the list state;
 * `environmentId` (the environment switcher) wins over the environment
 * filter.
 */
export function notificationQuery(
	filters: readonly ListFilter<Notification>[],
	s: ListFilterState,
	environmentId: string | null
): NotificationFilter {
	const value = (id: string) => {
		const f = filters.find((x) => x.id === id);
		return f ? activeValue(f, s.values[id]) : '';
	};
	return {
		kind: (value('kind') || undefined) as NotificationKind | undefined,
		outcome: (value('outcome') || undefined) as NotificationOutcome | undefined,
		environmentId: environmentId ?? (value('environment') || undefined)
	};
}

/** The words a notification is found by. */
export function notificationSearch(
	envName?: (id: string) => string | undefined
): (n: Notification) => (string | undefined)[] {
	return (n) => [
		n.title,
		n.detail,
		n.environmentId ? envName?.(n.environmentId) : undefined,
		notificationKindLabel(n.kind),
		outcomeLabel(n.outcome),
		...(n.fields ?? []).map((f) => f.value)
	];
}
