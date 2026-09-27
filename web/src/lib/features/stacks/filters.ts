// Search and filters of the stack list (ListCard, like the resource
// lists): status, pending changes (undeployed changes, image updates) and
// the environment while every environment is shown. Pure; tested in
// filters.spec.ts.
import {
	environmentFilter,
	statusOptions,
	type FilterContext,
	type ListFilter
} from '$lib/features/resources/filters';
import { stackStatus } from './model';
import type { Stack } from './queries';

export interface StackFilterContext extends FilterContext {
	/** Update state per stack ID ("update_available", "up_to_date"). */
	updates: ReadonlyMap<string, string | undefined>;
}

export function stackFilters(ctx: StackFilterContext): ListFilter<Stack>[] {
	const filters: ListFilter<Stack>[] = [
		{
			id: 'status',
			label: 'Status',
			all: 'All statuses',
			options: statusOptions([
				'running',
				'partial',
				'stopped',
				'missing',
				'deployed',
				'down',
				'failed',
				'undeployed'
			]),
			match: (s, v) => stackStatus(s) === v
		},
		{
			id: 'changes',
			label: 'Changes',
			all: 'All changes',
			options: [
				{ value: 'undeployed', label: 'Undeployed changes' },
				{ value: 'update', label: 'Update available' },
				{ value: 'none', label: 'No pending changes' }
			],
			match: (s, v) => {
				const update = ctx.updates.get(s.id) === 'update_available';
				if (v === 'undeployed') return !!s.undeployedChanges;
				if (v === 'update') return update;
				return !s.undeployedChanges && !update;
			}
		}
	];
	if (ctx.envs.length) filters.push(environmentFilter<Stack>(ctx.envs));
	return filters;
}

export const stackSearch = (s: Stack) => [s.name, s.displayName, s.description];
