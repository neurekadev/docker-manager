// Dashboard aggregates (#5, #22). Pure: the overview's per-environment
// usage and counts, recent jobs, stacks and update policies in; totals out.
// Units follow #5: CPU is a percentage of each environment's cores, so the
// cross-environment figure is the average of the environments with a
// sample (and the busiest one is named), memory is summed bytes.
import type { Job, Schema, Stack, UpdatePolicy } from '$lib/api/client';

type Env = Schema<'OverviewEnvironment'>;

export interface DashboardTotals {
	online: number;
	offline: number;
	/** Environments whose Docker counts are known. */
	counted: number;
	containers: number;
	running: number;
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
