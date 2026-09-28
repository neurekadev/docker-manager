// Search and filters of the build history and the build definitions (#33;
// ListCard, like the resource lists): the result and, while several
// environments are shown, the environment. Pure; tested in source.spec.ts.
import type { BuildDefinition, ImageBuild } from '$lib/api/queries';
import {
	environmentFilter,
	type FilterContext,
	type ListFilter
} from '$lib/features/resources/filters';
import { repoLabel } from './source';

/** Build results a user tells apart. */
const RESULTS: { value: string; label: string; states: string[] }[] = [
	{ value: 'running', label: 'Running', states: ['queued', 'running'] },
	{ value: 'succeeded', label: 'Succeeded', states: ['succeeded'] },
	{ value: 'failed', label: 'Failed', states: ['failed', 'interrupted'] },
	{ value: 'cancelled', label: 'Cancelled', states: ['cancelled'] }
];

export function buildFilters(ctx: FilterContext): ListFilter<ImageBuild>[] {
	const filters: ListFilter<ImageBuild>[] = [
		{
			id: 'status',
			label: 'Result',
			all: 'Any result',
			options: RESULTS.map(({ value, label }) => ({ value, label })),
			match: (b, v) => !!RESULTS.find((r) => r.value === v)?.states.includes(b.status)
		}
	];
	if (ctx.envs.length) filters.push(environmentFilter<ImageBuild>(ctx.envs));
	return filters;
}

/** Image names, repository, ref, commit and environment. */
export function buildSearch(
	envName: (id: string) => string | undefined
): (b: ImageBuild) => (string | undefined)[] {
	return (b) => [
		...b.tags,
		repoLabel(b.gitUrl),
		b.ref,
		b.resolvedRef,
		b.resolvedCommit,
		envName(b.environmentId)
	];
}

export function definitionFilters(ctx: FilterContext): ListFilter<BuildDefinition>[] {
	return ctx.envs.length ? [environmentFilter<BuildDefinition>(ctx.envs)] : [];
}

/** Name, description, repository, image names and environment. */
export function definitionSearch(
	envName: (id: string) => string | undefined
): (d: BuildDefinition) => (string | undefined)[] {
	return (d) => [
		d.name,
		d.description,
		d.source ? repoLabel(d.source.gitUrl) : undefined,
		d.source?.ref,
		...(d.source?.tags ?? []),
		envName(d.environmentId)
	];
}
