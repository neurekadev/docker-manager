// Search and filters of the jobs list (#26; ListCard, like the resource
// lists): state, kind, the environment while every environment is shown
// and, while set (a policy's "Open jobs", ?policyId=), the policy. The
// server applies them (GET /jobs is paged) and the same filters check the
// loaded rows; the search runs over the loaded jobs' words (kind, target,
// environment, origin, state). The count says how many jobs match when the
// server knows (`total`). Pure; tested in filters.spec.ts.
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

/** The policy a jobs list is narrowed to (?policyId=), with its name when known. */
export interface JobPolicy {
	id: string;
	name: string;
}

export function jobFilters(ctx: FilterContext & { policy?: JobPolicy }): ListFilter<Job>[] {
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
	// Only while set: its one option is the policy, "All policies" clears it.
	if (ctx.policy)
		filters.push({
			id: 'policy',
			label: 'Policy',
			all: 'All policies',
			dynamic: true,
			options: [{ value: ctx.policy.id, label: ctx.policy.name }],
			match: (j, v) => j.policyId === v
		});
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
		environmentId: environmentId ?? value('environment'),
		policyId: value('policy')
	};
}

const count = (n: number) => n.toLocaleString('en');
const jobs = (n: number) => `${count(n)} ${n === 1 ? 'job' : 'jobs'}`;

/**
 * The jobs list's count: "50 of 1,234 jobs" while more pages exist and the
 * server knows the total (jobs matching the filters), else "50 jobs
 * loaded"; "3 of the 50 loaded jobs (1,234 in all)" while a search covers
 * only the loaded pages; "12 of 40 jobs" / "40 jobs" once all are loaded.
 */
export function jobsSummary(o: {
	/** Rows shown (after the search). */
	shown: number;
	/** Rows loaded. */
	loaded: number;
	/** Jobs matching the server-side filters, when exact. */
	total?: number;
	/** More pages exist. */
	more: boolean;
	searching: boolean;
	filtered: boolean;
}): string {
	const all = o.total !== undefined ? ` (${count(o.total)} in all)` : '';
	if (o.more && o.searching)
		return `${count(o.shown)} of the ${count(o.loaded)} loaded jobs${all}`;
	const part = o.filtered && o.shown !== o.loaded;
	if (o.more && o.total !== undefined) return `${count(o.shown)} of ${jobs(o.total)}`;
	const text = part ? `${count(o.shown)} of ${jobs(o.loaded)}` : jobs(o.loaded);
	return o.more ? `${text} loaded` : text;
}

/** What a search that found nothing covered: "Searched 50 of the 1,234 jobs." */
export function jobsSearchedText(loaded: number, total?: number): string {
	return total !== undefined && total > loaded
		? `Searched ${count(loaded)} of the ${jobs(total)}.`
		: `Searched the ${count(loaded)} loaded ${loaded === 1 ? 'job' : 'jobs'}.`;
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
