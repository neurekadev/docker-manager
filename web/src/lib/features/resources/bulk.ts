// Bulk actions of the resource lists (#6, #32, #22 polish): which of the
// selected objects an action runs on, which are refused up front (Docker
// Manager's own objects, stack-managed containers, objects in use) and
// which are skipped because the action does not apply (a running
// container cannot be started), and the one summary toast of the whole
// selection. The server still decides every request; its refusals join
// the summary. Pure; unit-tested in bulk.spec.ts.
import type { Container, Image, Network, Volume } from '$lib/api/queries';
import { containerActions } from './container-actions';
import { can } from './permissions';
import { plain } from './refusals';

export type ContainerBulkVerb = 'start' | 'stop' | 'restart' | 'remove';

/** One object the action is not run on, with the reason users read. */
export interface BulkRefusal<T> {
	item: T;
	reason: string;
}

export interface BulkPlan<T> {
	/** The objects the action runs on. */
	run: T[];
	/** Refused up front (protected, managed by a stack, in use), with the reason. */
	refused: BulkRefusal<T>[];
	/** The action does not apply to them (state, or not granted). */
	skipped: T[];
}

/** Why Docker Manager's own containers are left out (#32). */
const PROTECTED_REASON: Record<ContainerBulkVerb, string> = {
	start: '',
	stop: 'Part of Docker Manager, which never stops its own containers.',
	restart: 'Part of Docker Manager: restart it from its own page, which asks you to confirm.',
	remove: 'Part of Docker Manager, which never removes its own containers.'
};

/** What a bulk container action runs on (#32: Docker Manager's own containers are refused). */
export function planContainers(
	containers: readonly Container[],
	verb: ContainerBulkVerb
): BulkPlan<Container> {
	const plan: BulkPlan<Container> = { run: [], refused: [], skipped: [] };
	for (const c of containers) {
		if (c.protection && verb !== 'start') {
			plan.refused.push({ item: c, reason: PROTECTED_REASON[verb] });
			continue;
		}
		if (verb === 'remove' && c.stack?.managed) {
			plan.refused.push({
				item: c,
				reason: `Belongs to the stack ${c.stack.project}: remove it through the stack.`
			});
			continue;
		}
		if (!containerActions(c).some((a) => a.verb === verb)) {
			plan.skipped.push(c);
			continue;
		}
		plan.run.push(c);
	}
	return plan;
}

type Removable = Pick<Image | Volume | Network, 'actions' | 'protection'>;

function planRemoval<T extends Removable>(
	items: readonly T[],
	capability: string,
	refuse: (item: T) => string | null
): BulkPlan<T> {
	const plan: BulkPlan<T> = { run: [], refused: [], skipped: [] };
	for (const item of items) {
		if (!can(item.actions, capability)) {
			plan.skipped.push(item);
			continue;
		}
		const reason = item.protection
			? `Docker Manager uses it (${plain(item.protection.reason)}), so it is never removed.`
			: refuse(item);
		if (reason) plan.refused.push({ item, reason });
		else plan.run.push(item);
	}
	return plan;
}

/** Images to remove: those containers still use are refused. */
export function planImageRemoval(images: readonly Image[]): BulkPlan<Image> {
	return planRemoval(images, 'image.remove', (im) =>
		im.usedBy?.length || im.inUse ? 'Containers still use it.' : null
	);
}

/** Volumes to remove: those containers still mount are refused. */
export function planVolumeRemoval(volumes: readonly Volume[]): BulkPlan<Volume> {
	return planRemoval(volumes, 'volume.remove', (v) =>
		v.usedBy?.length || v.inUse ? 'Containers still mount it.' : null
	);
}

/**
 * Networks to remove: predefined ones and those containers are attached
 * to (`attached`: `<environment>/<network>` keys, when known) are refused.
 */
export function planNetworkRemoval(
	networks: readonly Network[],
	attached?: ReadonlySet<string>
): BulkPlan<Network> {
	return planRemoval(networks, 'network.remove', (n) => {
		if (n.builtin) return 'Docker needs its predefined networks.';
		if (attached?.has(`${n.environmentId}/${n.name}`))
			return 'Containers are still attached to it.';
		return null;
	});
}

/** The outcome of a bulk action, counted when every job has ended. */
export interface BulkOutcome {
	succeeded: string[];
	/** Accepted jobs that failed, and requests the server refused. */
	failed: string[];
	/** Refused before any request (plan.refused), with the reason. */
	refused: { name: string; reason: string }[];
	skipped: number;
}

export interface BulkSummary {
	tone: 'success' | 'warn' | 'error';
	title: string;
	body?: string;
}

const PAST: Record<string, string> = {
	start: 'Started',
	stop: 'Stopped',
	restart: 'Restarted',
	remove: 'Removed'
};

const count = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** Names in a sentence: "a, b and 3 more". */
export function nameList(names: readonly string[], max = 3): string {
	if (names.length <= max) {
		if (names.length <= 1) return names.join('');
		return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
	}
	return `${names.slice(0, max).join(', ')} and ${names.length - max} more`;
}

/**
 * The one toast of a bulk action: "Stopped 3 containers", then what
 * did not happen and why ("2 failed: web and db. Left out docker-agent.
 * Part of Docker Manager, …").
 */
export function bulkSummary(
	verb: string,
	noun: { one: string; many: string },
	o: BulkOutcome
): BulkSummary {
	const past = PAST[verb] ?? verb;
	const parts: string[] = [];
	if (o.failed.length) parts.push(`${o.failed.length} failed: ${nameList(o.failed)}.`);
	if (o.refused.length === 1) parts.push(`Left out ${o.refused[0].name}. ${o.refused[0].reason}`);
	else if (o.refused.length) {
		const reasons = new Set(o.refused.map((r) => r.reason));
		parts.push(
			`Left out ${nameList(o.refused.map((r) => r.name))}` +
				(reasons.size === 1
					? `. ${[...reasons][0]}`
					: '. They are part of Docker Manager, managed by a stack or in use.')
		);
	}
	if (o.skipped)
		parts.push(
			`${o.skipped} skipped: the action does not apply to ${o.skipped === 1 ? 'it' : 'them'}.`
		);
	const body = parts.join(' ') || undefined;
	if (o.succeeded.length === 0)
		return {
			tone: 'error',
			title: `No ${noun.many} were ${past.toLowerCase()}.`,
			body
		};
	return {
		tone: o.failed.length || o.refused.length ? 'warn' : 'success',
		title: `${past} ${count(o.succeeded.length, noun.one, noun.many)}`,
		body
	};
}
