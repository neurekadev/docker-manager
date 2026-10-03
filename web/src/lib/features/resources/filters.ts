// Search and filters of the resource lists (containers, images, volumes,
// networks; stacks, jobs and schedules build theirs next to their
// features): one filter model rendered by ListCard, the stored state of a
// list (parsed defensively) and the filters each list offers. Text
// attributes (names, images, digests, labels, addresses) go into the
// search; a list offers selects for its few status-like attributes and
// switches for yes/no narrowing (unused, managed), all off by default.
// Pure; unit-tested in filters.spec.ts.
import type { Container, Image, Network, Volume } from '$lib/api/queries';
import { statusInfo } from '$lib/ui/status';
import { containerStatus } from './model';

export interface FilterOption {
	value: string;
	label: string;
}

/**
 * One filter of a list: a select of options, a free text, or a switch
 * (on narrows the list; its stored value is "on").
 */
export interface ListFilter<T> {
	/** Key in the stored state ("status", "stack"). */
	id: string;
	/** Accessible name of the control ("Status"); a switch's visible label ("Unused"). */
	label: string;
	kind?: 'select' | 'text' | 'switch';
	/**
	 * The option that filters nothing ("All Statuses"); the placeholder of
	 * a text filter; the tooltip of a switch ("Only volumes no container uses").
	 */
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

/** The stored value of a switch filter that is on. */
export const SWITCH_ON = 'on';

/** The value a filter applies ('' when it filters nothing or the value is unknown). */
export function activeValue<T>(f: ListFilter<T>, value: string | undefined): string {
	if (!value) return '';
	if (f.kind === 'text') return value.trim();
	if (f.kind === 'switch') return value === SWITCH_ON ? SWITCH_ON : '';
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
export function environmentFilter<T extends { environmentId?: string }>(
	envs: readonly Env[]
): ListFilter<T> {
	return {
		id: 'environment',
		label: 'Environment',
		all: 'All Environments',
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
		all: 'All Stacks',
		dynamic: true,
		options: [
			{ value: '-', label: 'No Stack (Standalone)' },
			...distinctOptions(rows.map((r) => r.stack?.project))
		],
		match: (r, v) => (v === '-' ? !r.stack : r.stack?.project === v)
	};
}

/** A yes/no filter shown as a switch (off: every row). */
export function switchFilter<T>(
	id: string,
	label: string,
	tooltip: string,
	match: (row: T) => boolean
): ListFilter<T> {
	return { id, label, kind: 'switch', all: tooltip, match: (r) => match(r) };
}

/** Rows no container uses (images, volumes). `plural`: "images". */
export function unusedFilter<T extends { inUse?: boolean; usedBy?: readonly unknown[] }>(
	plural: string
): ListFilter<T> {
	return switchFilter<T>(
		'unused',
		'Unused',
		`Only ${plural} no container uses`,
		(r) => !r.inUse && !r.usedBy?.length
	);
}

/**
 * Objects Docker Manager manages: those of its stacks and its own (#32).
 * `plural`: "volumes".
 */
export function managedFilter<
	T extends { stack?: { managed?: boolean; stackId?: string }; protection?: unknown }
>(plural: string): ListFilter<T> {
	return switchFilter<T>(
		'managed',
		'Managed',
		`Only ${plural} of Docker Manager stacks and of Docker Manager itself`,
		(r) => !!(r.stack?.managed || r.stack?.stackId || r.protection)
	);
}

/** The "key=value" texts of labels (the search matches keys, values and pairs). */
export function labelTexts(labels: Record<string, string> | undefined): string[] {
	return Object.entries(labels ?? {}).map(([k, v]) => `${k}=${v}`);
}

/** Options with the badge vocabulary's labels ("partial" → "Partially Running"). */
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

/** The container status filter value matching every state but running. */
export const NOT_RUNNING = 'not_running';

export function containerFilters(
	rows: readonly Container[],
	ctx: FilterContext
): ListFilter<Container>[] {
	return withEnv<Container>(
		[
			{
				id: 'status',
				label: 'Status',
				all: 'All Statuses',
				// "Not Running" is every state but running (what the
				// dashboard counts as not running).
				options: [
					...statusOptions(['running']),
					{ value: NOT_RUNNING, label: 'Not Running' },
					...statusOptions([
						'unhealthy',
						'paused',
						'restarting',
						'exited',
						'created',
						'dead'
					])
				],
				match: (c, v) =>
					v === NOT_RUNNING
						? c.state !== 'running'
						: v === 'unhealthy'
							? containerStatus(c) === v
							: c.state === v
			},
			stackFilter(rows),
			switchFilter<Container>(
				'updates',
				'Updates',
				'Only containers with an image update available',
				(c) => c.update === 'update_available'
			)
		],
		ctx
	);
}

/** Name, image, image digest, labels ("key=value"), networks, addresses and stack. */
export const containerSearch = (c: Container) => [
	c.name,
	c.image,
	c.imageId,
	c.id,
	c.stack?.project,
	c.stack?.service,
	...labelTexts(c.labels),
	...(c.networks ?? []).flatMap((n) => [n.name, n.ipAddress, n.ipv6Address])
];

// Images.

export function imageFilters(ctx: FilterContext): ListFilter<Image>[] {
	return withEnv<Image>([unusedFilter('images')], ctx);
}

/** Tags, ID, digests and labels. */
export const imageSearch = (im: Image) => [
	...im.repoTags,
	im.id,
	...(im.repoDigests ?? []),
	...labelTexts(im.labels)
];

// Volumes.

export function volumeFilters(rows: readonly Volume[], ctx: FilterContext): ListFilter<Volume>[] {
	return withEnv<Volume>(
		[
			{
				id: 'driver',
				label: 'Driver',
				all: 'All Drivers',
				dynamic: true,
				options: distinctOptions(rows.map((vol) => vol.driver)),
				match: (vol, v) => vol.driver === v
			},
			stackFilter(rows),
			unusedFilter('volumes'),
			managedFilter('volumes')
		],
		ctx
	);
}

/** Name, stack, driver, the containers using it and labels. */
export const volumeSearch = (v: Volume) => [
	v.name,
	v.stack?.project,
	v.driver,
	...(v.usedBy ?? []).map((c) => c.name),
	...labelTexts(v.labels)
];

// Networks.

/**
 * `used`: `<environment>/<network>` of the networks a container is
 * attached to (lists do not report attachments); undefined while the
 * containers are not readable (no Unused switch then).
 */
export function networkFilters(
	rows: readonly Network[],
	ctx: FilterContext & { used?: ReadonlySet<string> }
): ListFilter<Network>[] {
	const used = ctx.used;
	return withEnv<Network>(
		[
			{
				id: 'driver',
				label: 'Driver',
				all: 'All Drivers',
				dynamic: true,
				options: distinctOptions(rows.map((n) => n.driver)),
				match: (n, v) => n.driver === v
			},
			stackFilter(rows),
			...(used
				? [
						switchFilter<Network>(
							'unused',
							'Unused',
							'Only networks no container is attached to',
							(n) => !used.has(`${n.environmentId}/${n.name}`)
						)
					]
				: []),
			managedFilter('networks')
		],
		ctx
	);
}

/** Name, ID, subnets, gateways, driver, stack and labels. */
export const networkSearch = (n: Network) => [
	n.name,
	n.id,
	n.driver,
	n.stack?.project,
	...(n.subnets ?? []),
	...(n.gateways ?? []),
	...labelTexts(n.labels)
];

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
