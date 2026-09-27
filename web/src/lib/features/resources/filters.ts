// Search and filters of the resource lists (containers, images, volumes,
// networks; stacks build theirs in $lib/features/stacks/filters.ts): one
// filter model rendered by ListCard, the stored state of a list (parsed
// defensively) and the filters each list offers. Pure; unit-tested in
// filters.spec.ts.
import type { Container, Image, Network, Volume } from '$lib/api/queries';
import { statusInfo } from '$lib/ui/status';
import { containerStatus, volumeAccess } from './model';

export interface FilterOption {
	value: string;
	label: string;
}

/** One filter of a list: a select of options, or a free text (labels). */
export interface ListFilter<T> {
	/** Key in the stored state ("status", "stack"). */
	id: string;
	/** Accessible name of the control ("Status"). */
	label: string;
	kind?: 'select' | 'text';
	/** The option that filters nothing ("All statuses"); the placeholder of a text filter. */
	all: string;
	options?: FilterOption[];
	/**
	 * Options come from the rows (projects, drivers, environments): a stored
	 * value that is not among them still applies and stays selectable. A
	 * static filter ignores values outside its options.
	 */
	dynamic?: boolean;
	match: (row: T, value: string) => boolean;
}

/** The search text and filter values of one list. */
export interface ListFilterState {
	q: string;
	values: Record<string, string>;
}

export const emptyFilterState = (): ListFilterState => ({ q: '', values: {} });

/** The value a filter applies ('' when it filters nothing or the value is unknown). */
export function activeValue<T>(f: ListFilter<T>, value: string | undefined): string {
	if (!value) return '';
	if (f.kind === 'text') return value.trim();
	if (f.dynamic || (f.options ?? []).some((o) => o.value === value)) return value;
	return '';
}

/** Whether the search or any of these filters narrows the list. */
export function isFiltering<T>(filters: readonly ListFilter<T>[], s: ListFilterState): boolean {
	return s.q.trim() !== '' || filters.some((f) => activeValue(f, s.values[f.id]) !== '');
}

/**
 * The rows matching the search (case-insensitive, on any of the texts
 * `search` returns) and every active filter.
 */
export function applyListFilters<T>(
	rows: readonly T[],
	filters: readonly ListFilter<T>[],
	s: ListFilterState,
	search: (row: T) => readonly (string | undefined)[]
): T[] {
	const q = s.q.trim().toLowerCase();
	const active = filters.flatMap((f) => {
		const v = activeValue(f, s.values[f.id]);
		return v ? [[f, v] as const] : [];
	});
	return rows.filter(
		(r) =>
			(!q || search(r).some((t) => t?.toLowerCase().includes(q))) &&
			active.every(([f, v]) => f.match(r, v))
	);
}

/** The options of a select: "all" first, then the options, then a stored value no longer listed. */
export function selectOptions<T>(f: ListFilter<T>, value: string | undefined): FilterOption[] {
	const opts = [{ value: '', label: f.all }, ...(f.options ?? [])];
	const v = activeValue(f, value);
	if (v && !opts.some((o) => o.value === v)) opts.push({ value: v, label: v });
	return opts;
}

/**
 * The filters worth showing: a filter built from the rows is hidden while
 * it offers fewer than two choices (one driver, one environment), unless
 * it is set.
 */
export function visibleFilters<T>(
	filters: readonly ListFilter<T>[],
	s: ListFilterState
): ListFilter<T>[] {
	return filters.filter(
		(f) => !f.dynamic || (f.options?.length ?? 0) >= 2 || activeValue(f, s.values[f.id]) !== ''
	);
}

/** Distinct non-empty values as sorted options. */
export function distinctOptions(
	values: Iterable<string | undefined>,
	label: (v: string) => string = (v) => v
): FilterOption[] {
	return [...new Set([...values].filter((v): v is string => !!v))]
		.sort((a, b) => a.localeCompare(b))
		.map((v) => ({ value: v, label: label(v) }));
}

// Stored state (sessionStorage, list-filters.svelte.ts). Never trusted:
// anything that is not a short string is dropped.

const MAX_TEXT = 200;
const MAX_FILTERS = 20;
const ID_RE = /^[a-z][a-z0-9-]{0,39}$/;

/** Parses a stored list state; corrupt or unexpected input yields the empty state. */
export function parseFilterState(raw: string | null | undefined): ListFilterState {
	const out = emptyFilterState();
	if (!raw) return out;
	let data: unknown;
	try {
		data = JSON.parse(raw);
	} catch {
		return out;
	}
	if (!data || typeof data !== 'object' || Array.isArray(data)) return out;
	const { q, values } = data as { q?: unknown; values?: unknown };
	if (typeof q === 'string' && q.length <= MAX_TEXT) out.q = q;
	if (values && typeof values === 'object' && !Array.isArray(values))
		for (const [k, v] of Object.entries(values).slice(0, MAX_FILTERS))
			if (ID_RE.test(k) && typeof v === 'string' && v !== '' && v.length <= MAX_TEXT)
				out.values[k] = v;
	return out;
}

/** The stored form of a list state; null when there is nothing to keep. */
export function serializeFilterState(s: ListFilterState): string | null {
	const values = Object.fromEntries(Object.entries(s.values).filter(([, v]) => v !== ''));
	if (s.q === '' && Object.keys(values).length === 0) return null;
	return JSON.stringify({ q: s.q, values });
}

// Shared filters.

interface Env {
	id: string;
	name: string;
}

/** Environment (only while every environment is shown). */
export function environmentFilter<T extends { environmentId: string }>(
	envs: readonly Env[]
): ListFilter<T> {
	return {
		id: 'environment',
		label: 'Environment',
		all: 'All environments',
		dynamic: true,
		options: [...envs]
			.sort((a, b) => a.name.localeCompare(b.name))
			.map((e) => ({ value: e.id, label: e.name })),
		match: (r, v) => r.environmentId === v
	};
}

/** Compose project ('-': objects of no project). */
export function stackFilter<T extends { stack?: { project: string } }>(
	rows: readonly T[]
): ListFilter<T> {
	return {
		id: 'stack',
		label: 'Stack',
		all: 'All stacks',
		dynamic: true,
		options: [
			{ value: '-', label: 'No stack (standalone)' },
			...distinctOptions(rows.map((r) => r.stack?.project))
		],
		match: (r, v) => (v === '-' ? !r.stack : r.stack?.project === v)
	};
}

/** Docker Manager's own objects (#32). `plural`: "containers". */
export function systemFilter<T extends { protection?: unknown }>(plural: string): ListFilter<T> {
	return {
		id: 'system',
		label: 'Docker Manager system',
		all: `Show system ${plural}`,
		options: [
			{ value: 'only', label: `Only system ${plural}` },
			{ value: 'hide', label: `Hide system ${plural}` }
		],
		match: (r, v) => (v === 'only') === !!r.protection
	};
}

/** Used by at least one container (images, volumes). */
export function usageFilter<T extends { inUse?: boolean }>(): ListFilter<T> {
	return {
		id: 'usage',
		label: 'Usage',
		all: 'Used and unused',
		options: [
			{ value: 'used', label: 'Used by containers' },
			{ value: 'unused', label: 'Unused' }
		],
		match: (r, v) => (v === 'used') === !!r.inUse
	};
}

/** A "key" or "key=value" label filter. */
export function labelMatches(labels: Record<string, string> | undefined, filter: string): boolean {
	const f = filter.trim();
	if (!f) return true;
	const eq = f.indexOf('=');
	const key = (eq < 0 ? f : f.slice(0, eq)).trim();
	const v = labels?.[key];
	if (v === undefined) return false;
	return eq < 0 || v === f.slice(eq + 1).trim();
}

/** Labels (full view only). */
export function labelFilter<T extends { labels?: Record<string, string> }>(): ListFilter<T> {
	return {
		id: 'label',
		label: 'Label',
		kind: 'text',
		all: 'Label: key or key=value',
		match: (r, v) => labelMatches(r.labels, v)
	};
}

/** Options with the badge vocabulary's labels ("partial" → "Partially running"). */
export const statusOptions = (values: readonly string[]): FilterOption[] =>
	values.map((v) => ({ value: v, label: statusInfo(v).label }));

/** What each list offers besides the environment (`envs` empty: one environment is shown). */
export interface FilterContext {
	envs: readonly Env[];
}

const withEnv = <T extends { environmentId: string }>(
	filters: ListFilter<T>[],
	ctx: FilterContext
): ListFilter<T>[] => (ctx.envs.length ? [...filters, environmentFilter<T>(ctx.envs)] : filters);

// Containers.

const UPDATE_PROBLEMS = new Set(['check_failed', 'run_failed', 'quarantined']);

export function containerFilters(
	rows: readonly Container[],
	ctx: FilterContext
): ListFilter<Container>[] {
	return withEnv<Container>(
		[
			{
				id: 'status',
				label: 'Status',
				all: 'All statuses',
				options: statusOptions([
					'running',
					'unhealthy',
					'paused',
					'restarting',
					'exited',
					'created',
					'dead'
				]),
				match: (c, v) => (v === 'unhealthy' ? containerStatus(c) === v : c.state === v)
			},
			stackFilter(rows),
			{
				id: 'update',
				label: 'Image update',
				all: 'All update states',
				options: [
					{ value: 'available', label: 'Update available' },
					{ value: 'current', label: 'Up to date' },
					{ value: 'problem', label: 'Check or update failed' },
					{ value: 'none', label: 'No update policy' }
				],
				match: (c, v) => {
					switch (v) {
						case 'available':
							return c.update === 'update_available';
						case 'current':
							return c.update === 'up_to_date';
						case 'problem':
							return !!c.update && UPDATE_PROBLEMS.has(c.update);
						default:
							return !c.update || c.update === 'ineligible';
					}
				}
			},
			systemFilter('containers'),
			labelFilter()
		],
		ctx
	);
}

export const containerSearch = (c: Container) => [
	c.name,
	c.image,
	c.stack?.project,
	c.stack?.service
];

// Images.

export function imageFilters(ctx: FilterContext): ListFilter<Image>[] {
	return withEnv<Image>(
		[
			usageFilter(),
			{
				id: 'tags',
				label: 'Tags',
				all: 'Tagged and untagged',
				options: [
					{ value: 'tagged', label: 'Tagged' },
					{ value: 'untagged', label: 'Untagged (dangling)' }
				],
				match: (im, v) => (v === 'tagged') === im.repoTags.length > 0
			},
			systemFilter('images')
		],
		ctx
	);
}

export const imageSearch = (im: Image) => [...im.repoTags, im.id, ...(im.repoDigests ?? [])];

// Volumes.

export function volumeFilters(rows: readonly Volume[], ctx: FilterContext): ListFilter<Volume>[] {
	return withEnv<Volume>(
		[
			usageFilter(),
			stackFilter(rows),
			{
				id: 'files',
				label: 'File access',
				all: 'Local and read-only',
				options: [
					{ value: 'local', label: 'Local (files open)' },
					{ value: 'readonly', label: 'Read-only' }
				],
				match: (vol, v) => (v === 'local') === volumeAccess(vol).local
			},
			{
				id: 'driver',
				label: 'Driver',
				all: 'All drivers',
				dynamic: true,
				options: distinctOptions(rows.map((vol) => vol.driver)),
				match: (vol, v) => vol.driver === v
			},
			systemFilter('volumes')
		],
		ctx
	);
}

export const volumeSearch = (v: Volume) => [v.name, v.stack?.project];

// Networks.

export function networkFilters(
	rows: readonly Network[],
	ctx: FilterContext
): ListFilter<Network>[] {
	return withEnv<Network>(
		[
			stackFilter(rows),
			{
				id: 'driver',
				label: 'Driver',
				all: 'All drivers',
				dynamic: true,
				options: distinctOptions(rows.map((n) => n.driver)),
				match: (n, v) => n.driver === v
			},
			{
				id: 'access',
				label: 'Access',
				all: 'Any access',
				options: [
					{ value: 'internal', label: 'Internal' },
					{ value: 'attachable', label: 'Attachable' },
					{ value: 'ipv6', label: 'IPv6' },
					{ value: 'default', label: 'Default' }
				],
				match: (n, v) => {
					switch (v) {
						case 'internal':
							return !!n.internal;
						case 'attachable':
							return !!n.attachable;
						case 'ipv6':
							return !!n.enableIpv6;
						default:
							return !n.internal && !n.attachable && !n.enableIpv6;
					}
				}
			},
			{
				id: 'scope',
				label: 'Scope',
				all: 'All scopes',
				dynamic: true,
				options: distinctOptions(rows.map((n) => n.scope)),
				match: (n, v) => n.scope === v
			},
			{
				id: 'predefined',
				label: 'Predefined',
				all: 'Show predefined networks',
				options: [
					{ value: 'only', label: 'Only predefined networks' },
					{ value: 'hide', label: 'Hide predefined networks' }
				],
				match: (n, v) => (v === 'only') === !!n.builtin
			},
			systemFilter('networks')
		],
		ctx
	);
}

export const networkSearch = (n: Network) => [n.name, ...(n.subnets ?? [])];

/** The count next to a list's title: "40 containers", "3 of 40 containers". */
export function listSummary(
	shown: number,
	total: number,
	filtered: boolean,
	one: string,
	many: string
): string {
	const noun = total === 1 ? one : many;
	return filtered ? `${shown} of ${total} ${noun}` : `${total} ${noun}`;
}
