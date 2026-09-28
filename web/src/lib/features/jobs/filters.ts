// Search and filters of the jobs list (#26; ListCard, like the resource
// lists): state, kind and the environment while every environment is
// shown. The server applies them (GET /jobs is paged) and the same
// filters check the loaded rows; the search runs over the loaded jobs'
// words (kind, target, environment, origin, state). Pure; tested in
// filters.spec.ts.
import type { Job } from '$lib/api/client';
import type { JobFilters } from '$lib/api/queries';
import {
	activeValue,
	environmentFilter,
	type FilterContext,
	type ListFilter,
	type ListFilterState
} from '$lib/features/resources/filters';
import { statusInfo } from '$lib/ui/status';
import {
	JOB_KIND_LABELS,
	ORIGIN_LABELS,
	STATE_FILTERS,
	jobKindLabel,
	jobTargetLabel,
	type NameOf
} from './labels';

export function jobFilters(ctx: FilterContext): ListFilter<Job>[] {
	const filters: ListFilter<Job>[] = [
		{
			id: 'state',
			label: 'State',
			all: 'All states',
			options: STATE_FILTERS.filter((s) => s.id).map((s) => ({
				value: s.id,
				label: s.label
			})),
			match: (j, v) => !!STATE_FILTERS.find((s) => s.id === v)?.states.includes(j.state)
		},
		{
			id: 'kind',
			label: 'Kind',
			all: 'All kinds',
			options: Object.entries(JOB_KIND_LABELS)
				.map(([value, label]) => ({ value, label }))
				.sort((a, b) => a.label.localeCompare(b.label)),
			match: (j, v) => j.kind === v
		}
	];
	if (ctx.envs.length) filters.push(environmentFilter<Job>(ctx.envs));
	return filters;
}

/**
 * The server-side filters of GET /jobs from the list state; `environmentId`
 * (the environment switcher) wins over the environment filter.
 */
export function jobQuery(
	filters: readonly ListFilter<Job>[],
	s: ListFilterState,
	environmentId: string | null
): JobFilters {
	const value = (id: string) => {
		const f = filters.find((x) => x.id === id);
		return f ? activeValue(f, s.values[id]) : '';
	};
	return {
		states: STATE_FILTERS.find((x) => x.id === value('state'))?.states ?? [],
		kind: value('kind'),
		environmentId: environmentId ?? value('environment')
	};
}

/** The words a job is found by: kind, target, environment, origin, state and ID. */
export function jobSearch(
	nameOf?: NameOf,
	envName?: (id: string) => string | undefined
): (j: Job) => (string | undefined)[] {
	return (j) => [
		jobKindLabel(j.kind),
		j.kind,
		jobTargetLabel(j, nameOf),
		...(j.targets ?? []).map((t) => t.id),
		j.environmentId ? envName?.(j.environmentId) : 'Docker Manager',
		ORIGIN_LABELS[j.origin] ?? j.origin,
		statusInfo(j.state, 'job').label,
		j.id
	];
}
