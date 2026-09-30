// Search and filters of the Alerts list (#159; ListCard, like the other
// section lists): the state (Active, the default, else Dismissed or
// Resolved), the kind and, while every environment is shown, the
// environment. The server applies them (GET /alerts) and the same filters
// check the loaded rows; the search runs over the title, the detail, the
// environment and the kind. `alertsPreset` builds the filters a link sets
// before it opens the list (the dashboard's "Needs attention", the System
// tab's alert marks). Pure; tested in filters.spec.ts.
import {
	activeValue,
	environmentFilter,
	type FilterContext,
	type ListFilter,
	type ListFilterState
} from '$lib/features/resources/filters';
import {
	ALERT_KINDS,
	alertView,
	alertCount,
	kindLabel,
	severityLabel,
	type Alert,
	type AlertKind,
	type AlertView
} from './model';
import type { AlertFilter } from './queries';

/** The list's name in the stored filters (ListFilters). */
export const ALERTS_LIST = 'alerts';

/**
 * The state filter: its "all" choice is Active (the default, and what
 * Clear filters returns to), the options the other states.
 */
export const STATE_FILTER_LABELS: Record<AlertView, string> = {
	active: 'Active',
	dismissed: 'Dismissed',
	resolved: 'Resolved'
};

export function alertFilters(ctx: FilterContext): ListFilter<Alert>[] {
	const filters: ListFilter<Alert>[] = [
		{
			id: 'state',
			label: 'State',
			all: STATE_FILTER_LABELS.active,
			options: [
				{ value: 'dismissed', label: STATE_FILTER_LABELS.dismissed },
				{ value: 'resolved', label: STATE_FILTER_LABELS.resolved }
			],
			match: (a, v) => alertView(a) === v
		},
		{
			id: 'kind',
			label: 'Kind',
			all: 'All kinds',
			options: ALERT_KINDS.map((k) => ({ value: k.kind, label: k.label })),
			match: (a, v) => a.kind === v
		}
	];
	if (ctx.envs.length) filters.push(environmentFilter<Alert>(ctx.envs));
	return filters;
}

/** The list's state from the filters: active unless Dismissed or Resolved is chosen. */
export function alertsView(filters: readonly ListFilter<Alert>[], s: ListFilterState): AlertView {
	const f = filters.find((x) => x.id === 'state');
	const v = f ? activeValue(f, s.values.state) : '';
	return v === 'dismissed' || v === 'resolved' ? v : 'active';
}

/**
 * The server-side filters of GET /alerts from the list state;
 * `environmentId` (the environment switcher) wins over the environment
 * filter.
 */
export function alertQuery(
	filters: readonly ListFilter<Alert>[],
	s: ListFilterState,
	environmentId: string | null
): AlertFilter {
	const value = (id: string) => {
		const f = filters.find((x) => x.id === id);
		return f ? activeValue(f, s.values[id]) : '';
	};
	return {
		state: alertsView(filters, s),
		kind: (value('kind') || undefined) as AlertKind | undefined,
		environmentId: environmentId ?? (value('environment') || undefined)
	};
}

/** The words an alert is found by: title, detail, environment, kind and severity. */
export function alertSearch(
	envName?: (id: string) => string | undefined
): (a: Alert) => (string | undefined)[] {
	return (a) => [
		a.title,
		a.detail,
		a.environmentId ? envName?.(a.environmentId) : undefined,
		kindLabel(a.kind),
		severityLabel(a.severity)
	];
}

/** The count next to the title: "3 alerts", "2 of 5 alerts" while the search narrows it. */
export function alertsSummary(shown: number, loaded: number): string {
	return shown === loaded ? alertCount(loaded) : `${shown} of ${alertCount(loaded)}`;
}

/** Filters a link sets on the Alerts list, and the environment to select first. */
export interface AlertsPreset {
	filters: { list: string; values: Record<string, string> };
	/** Select this environment in the switcher (it shows another one). */
	select?: string;
}

/**
 * The preset of a link that opens the Alerts list filtered (the dashboard,
 * the System tab): kind and state as given (Active leaves the state at
 * its default), the environment as a filter while every environment is
 * shown, or through the switcher while it shows another one.
 */
export function alertsPreset(
	o: { kind?: AlertKind; view?: AlertView; environmentId?: string },
	selected: string | null = null
): AlertsPreset {
	const values: Record<string, string> = {};
	if (o.view && o.view !== 'active') values.state = o.view;
	if (o.kind) values.kind = o.kind;
	let select: string | undefined;
	if (o.environmentId) {
		if (selected && selected !== o.environmentId) select = o.environmentId;
		else if (!selected) values.environment = o.environmentId;
	}
	return { filters: { list: ALERTS_LIST, values }, ...(select ? { select } : {}) };
}
