// Search and filters of the schedules list (#13; ListCard, like the
// resource lists): kind, state and, while every environment is shown,
// the environment ("Docker Manager" for the manager's own policies).
// Pure; tested in model.spec.ts.
import type { Schedule } from '$lib/api/client';
import type { ListFilter } from '$lib/features/resources/filters';
import { scheduleState } from './model';

interface Env {
	id: string;
	name: string;
}

/** `envs`: the environments while every environment is shown (else empty). */
export function scheduleFilters(
	rows: readonly Schedule[],
	ctx: { envs: readonly Env[] }
): ListFilter<Schedule>[] {
	const filters: ListFilter<Schedule>[] = [
		{
			id: 'kind',
			label: 'Kind',
			all: 'All kinds',
			dynamic: true,
			options: [...new Map(rows.map((s) => [s.kind, s.kindLabel])).entries()]
				.map(([value, label]) => ({ value, label }))
				.sort((a, b) => a.label.localeCompare(b.label)),
			match: (s, v) => s.kind === v
		},
		{
			id: 'state',
			label: 'State',
			all: 'Any state',
			options: [
				{ value: 'enabled', label: 'Enabled' },
				{ value: 'disabled', label: 'Disabled' },
				{ value: 'invalid', label: 'Invalid' }
			],
			match: (s, v) => scheduleState(s).label.toLowerCase() === v
		}
	];
	if (ctx.envs.length)
		filters.push({
			id: 'environment',
			label: 'Environment',
			all: 'All environments',
			dynamic: true,
			options: [
				{ value: '-', label: 'Docker Manager' },
				...ctx.envs
					.map((e) => ({ value: e.id, label: e.name }))
					.sort((a, b) => a.label.localeCompare(b.label))
			],
			match: (s, v) => (v === '-' ? !s.environmentId : s.environmentId === v)
		});
	return filters;
}

/** Policy name, kind, schedule, time zone and environment name. */
export function scheduleSearch(
	envName: (id: string) => string | undefined
): (s: Schedule) => (string | undefined)[] {
	return (s) => [
		s.policyName,
		s.kindLabel,
		s.cron,
		s.timeZone,
		s.environmentId ? envName(s.environmentId) : 'Docker Manager'
	];
}
