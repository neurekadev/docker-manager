// Lists that span environments (#6, #22). Docker objects are listed per
// environment by its own agent (GET /environments/{id}/containers, ...), so
// "All environments" asks every visible environment and merges the
// answers. Offline environments are reported instead of failing the whole
// view: their agent cannot answer (503 environment_offline), and the page
// says which environments are missing rather than showing a partial list
// as complete.
import type { Environment } from './client';
import { ApiRequestError } from './client';

/** The environments a list reads: the selected one, or every visible one. */
export interface EnvTarget {
	id: string;
	name: string;
	online: boolean;
	connectionChangedAt?: string;
}

export interface Unavailable {
	environment: EnvTarget;
	/** The agent is not connected (known offline or 503 environment_offline). */
	offline: boolean;
	/** Why it failed otherwise (shown with the environment's name). */
	error?: unknown;
}

export interface EnvList<T> {
	items: T[];
	unavailable: Unavailable[];
}

/** Active environments to read: the selected one or all (archived ones never). */
export function envTargets(environments: Environment[], selected: string | null): EnvTarget[] {
	return environments
		.filter((e) => e.status === 'active' && (!selected || e.id === selected))
		.map((e) => ({
			id: e.id,
			name: e.name,
			online: e.online,
			connectionChangedAt: e.connectionChangedAt
		}));
}

function isOffline(e: unknown): boolean {
	return e instanceof ApiRequestError && e.apiError?.code === 'environment_offline';
}

/**
 * Reads one list per environment in parallel. Offline environments and
 * failures are collected in `unavailable`; when every environment failed
 * for another reason than being offline, the first error is thrown so the
 * view shows its error state.
 */
export async function acrossEnvironments<T>(
	targets: EnvTarget[],
	fetchOne: (env: EnvTarget) => Promise<T[]>
): Promise<EnvList<T>> {
	const results = await Promise.all(
		targets.map(async (env): Promise<{ items: T[] } | Unavailable> => {
			if (!env.online) return { environment: env, offline: true };
			try {
				return { items: await fetchOne(env) };
			} catch (e) {
				return {
					environment: env,
					offline: isOffline(e),
					error: isOffline(e) ? undefined : e
				};
			}
		})
	);
	const items: T[] = [];
	const unavailable: Unavailable[] = [];
	for (const r of results) {
		if ('items' in r) items.push(...r.items);
		else unavailable.push(r);
	}
	const failed = unavailable.filter((u) => u.error !== undefined);
	if (targets.length > 0 && failed.length > 0 && failed.length === targets.length)
		throw failed[0].error;
	return { items, unavailable };
}

/** Follows nextCursor until the last page. */
export async function allPages<T>(
	fetchPage: (cursor: string | undefined) => Promise<{ items: T[]; nextCursor?: string }>,
	maxPages = 50
): Promise<T[]> {
	const out: T[] = [];
	let cursor: string | undefined;
	for (let i = 0; i < maxPages; i++) {
		const page = await fetchPage(cursor);
		out.push(...page.items);
		cursor = page.nextCursor;
		if (!cursor) break;
	}
	return out;
}
