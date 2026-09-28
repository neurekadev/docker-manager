// Dashboard aggregates (#5, #22). Pure: the overview's per-environment
// usage and counts, recent jobs, stacks and update policies in; totals out.
// What needs attention (failed jobs, containers not running, pending
// changes, offline environments) links to the list that shows it.
// Units follow #5: CPU is a percentage of each environment's cores, so the
// cross-environment figure is the average of the environments with a
// sample (and the busiest one is named), memory is summed bytes.
import type { Job, Schema, Stack, UpdatePolicy } from '$lib/api/client';
import { routes } from '$lib/routes';

type Env = Schema<'OverviewEnvironment'>;

export interface DashboardTotals {
	online: number;
	offline: number;
	/** Environments whose Docker counts are known. */
	counted: number;
	containers: number;
	running: number;
	paused: number;
	cpuAverage: number | null;
	cpuBusiest: { name: string; value: number } | null;
	memUsed: number;
	memTotal: number;
	/** Failed, partly failed or interrupted jobs of the last 24 hours. */
	failures: number;
	lastFailure: Job | null;
}

const PROBLEM_STATES = new Set(['failed', 'partial', 'interrupted']);

export function dashboardTotals(
	envs: readonly Env[],
	jobs: readonly Job[],
	now: number
): DashboardTotals {
	let online = 0;
	let counted = 0;
	let containers = 0;
	let running = 0;
	let paused = 0;
	let cpuSum = 0;
	let cpuN = 0;
	let busiest: { name: string; value: number } | null = null;
	let memUsed = 0;
	let memTotal = 0;
	for (const e of envs) {
		if (e.online) online++;
		if (e.docker) {
			counted++;
			containers += Math.max(0, e.docker.containers);
			running += Math.max(0, e.docker.containersRunning);
			paused += Math.max(0, e.docker.containersPaused);
		}
		// Offline environments keep their last sample: exclude it from "in use now".
		const u = e.online ? e.usage : undefined;
		if (u?.cpuPercent !== undefined) {
			cpuSum += u.cpuPercent;
			cpuN++;
			if (!busiest || u.cpuPercent > busiest.value)
				busiest = { name: e.name, value: u.cpuPercent };
		}
		if (u?.memoryUsedBytes !== undefined && u.memoryTotalBytes) {
			memUsed += u.memoryUsedBytes;
			memTotal += u.memoryTotalBytes;
		}
	}
	const dayAgo = now - 86_400_000;
	const failed = jobs.filter(
		(j) => PROBLEM_STATES.has(j.state) && Date.parse(j.createdAt) >= dayAgo
	);
	return {
		online,
		offline: envs.length - online,
		counted,
		containers,
		running,
		paused,
		cpuAverage: cpuN ? cpuSum / cpuN : null,
		cpuBusiest: cpuN > 1 ? busiest : null,
		memUsed,
		memTotal,
		failures: failed.length,
		lastFailure: failed[0] ?? null
	};
}

export interface EnvironmentCounts {
	stacks: number;
	undeployed: number;
	updates: number;
}

/** Stacks, stacks with undeployed changes and available updates per environment. */
export function perEnvironment(
	stacks: readonly Stack[] | undefined,
	policies: readonly UpdatePolicy[] | undefined
): Map<string, EnvironmentCounts> {
	const out = new Map<string, EnvironmentCounts>();
	const get = (id: string) => {
		let c = out.get(id);
		if (!c) out.set(id, (c = { stacks: 0, undeployed: 0, updates: 0 }));
		return c;
	};
	for (const s of stacks ?? []) {
		const c = get(s.environmentId);
		c.stacks++;
		if (s.undeployedChanges) c.undeployed++;
	}
	for (const p of policies ?? []) get(p.environmentId).updates += p.summary?.available ?? 0;
	return out;
}

/** One thing on the dashboard that needs a look, linking to the list that shows it. */
export interface AttentionItem {
	id: 'offline' | 'failed-jobs' | 'stopped-containers' | 'undeployed' | 'updates';
	label: string;
	href: string;
	tone: 'danger' | 'warn' | 'offline';
	/**
	 * Filters to set on the target list before following the link (the
	 * lists keep their filters per browser tab, ListFilters); absent: the
	 * list as it is.
	 */
	filters?: { list: string; values: Record<string, string> };
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/**
 * What needs attention across the shown environments, most urgent first:
 * offline environments, failed jobs of the last 24 hours, containers not
 * running, stacks with undeployed changes and available updates. Only
 * figures the caller can see count (`undefined` = not loaded or no access).
 */
export function attentionItems(
	t: Pick<DashboardTotals, 'offline' | 'failures' | 'counted' | 'containers' | 'running'> & {
		paused?: number;
	},
	pending: { undeployed?: number; updates?: number } = {}
): AttentionItem[] {
	const out: AttentionItem[] = [];
	if (t.offline > 0)
		out.push({
			id: 'offline',
			label: `${plural(t.offline, 'environment is', 'environments are')} offline`,
			href: routes.environments(),
			tone: 'offline'
		});
	if (t.failures > 0)
		out.push({
			id: 'failed-jobs',
			label: `${plural(t.failures, 'job', 'jobs')} failed in the last 24 hours`,
			href: routes.jobs(),
			tone: 'danger',
			filters: { list: 'jobs', values: { state: 'problems' } }
		});
	const stopped = t.counted ? t.containers - t.running : 0;
	if (stopped > 0)
		out.push({
			id: 'stopped-containers',
			label: `${plural(stopped, 'container is', 'containers are')} not running`,
			href: routes.containers(),
			tone: 'warn',
			// Stopped containers show as "exited"; paused ones have their own
			// status, so a mix opens the whole list.
			filters: t.paused ? undefined : { list: 'containers', values: { status: 'exited' } }
		});
	if (pending.undeployed)
		out.push({
			id: 'undeployed',
			label: `${plural(pending.undeployed, 'stack has', 'stacks have')} undeployed changes`,
			href: routes.stacks(),
			tone: 'warn',
			filters: { list: 'stacks', values: { changes: 'undeployed' } }
		});
	if (pending.updates)
		out.push({
			id: 'updates',
			label: `${plural(pending.updates, 'update is', 'updates are')} available`,
			href: routes.updates(),
			tone: 'warn'
		});
	return out;
}

/** Undeployed stacks and available updates of the shown environments. */
export function pendingChanges(
	counts: ReadonlyMap<string, EnvironmentCounts>,
	environmentIds: readonly string[]
): { undeployed: number; updates: number } {
	let undeployed = 0;
	let updates = 0;
	for (const id of environmentIds) {
		const c = counts.get(id);
		if (!c) continue;
		undeployed += c.undeployed;
		updates += c.updates;
	}
	return { undeployed, updates };
}
