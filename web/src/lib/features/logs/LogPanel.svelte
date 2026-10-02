<script lang="ts" module>
	export type LogTarget =
		| { kind: 'stack'; stackId: string }
		| { kind: 'container'; environmentId: string; containerId: string };
</script>

<script lang="ts">
	// Logs of a stack (every service container, #8: there is no stack-level
	// log route; each container's own stream is followed and merged) or of
	// one container. Owns the LogFeed and hands it to LogViewer.
	import { createQuery } from '@tanstack/svelte-query';
	import { onDestroy, untrack } from 'svelte';
	import type { Snippet } from 'svelte';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import { api, ApiRequestError, unwrap } from '$lib/api/client';
	import { SERVICE_COLOR } from '$lib/design/hue';
	import { stackQuery, stackServicesQuery } from '$lib/features/files/resources';
	import { routes } from '$lib/routes';
	import { EmptyState, ErrorState, Skeleton, errorMessage } from '$lib/ui';
	import { LogFeed, type LogSource } from './feed.svelte';
	import LogViewer from './LogViewer.svelte';

	interface Props {
		target: LogTarget;
		/** Display name (stack or container). */
		name: string;
		dense?: boolean;
		/** Offer "Open in a New Window" (not inside the window itself). */
		popout?: boolean;
		extra?: Snippet;
		/** Stack logs: start with only this service selected (?service=<name>). */
		service?: string | null;
	}

	let { target, name, dense = false, popout = true, extra, service = null }: Props = $props();

	function readLogs(s: LogSource, q: { tail?: number; since?: string }) {
		return unwrap(
			api.GET('/api/v1/environments/{environmentId}/containers/{containerId}/logs', {
				params: {
					path: { environmentId: s.environmentId, containerId: s.containerId },
					query: q
				}
			})
		);
	}

	function explain(e: unknown): { status: number | null; message: string } {
		const status = e instanceof ApiRequestError ? e.status : null;
		if (status === 403)
			return {
				status,
				message: `You can't read these logs. Ask the owner of this Docker Manager for “View logs”.`
			};
		if (status === 404) return { status, message: 'The container does not exist anymore.' };
		if (status === 503)
			return {
				status,
				message: 'The environment is offline. The logs continue when it reconnects.'
			};
		return { status, message: errorMessage(e) };
	}

	async function probe(s: LogSource): Promise<{ status: number | null; message: string }> {
		try {
			await readLogs(s, { tail: 1 });
			return {
				status: null,
				message: 'The log stream was interrupted. It reconnects on its own.'
			};
		} catch (e) {
			return explain(e);
		}
	}

	async function poll(s: LogSource, since?: string) {
		try {
			return (await readLogs(s, since ? { since } : { tail: 200 })).lines;
		} catch (e) {
			const x = explain(e);
			throw Object.assign(new Error(x.message), { status: x.status });
		}
	}

	// HTTP/2 and HTTP/3 multiplex streams; over HTTP/1.1 the browser keeps six
	// connections per host for all tabs together (each tab's live stream holds
	// one): stream one container per viewer and poll the rest, so a pop-out
	// window and the page beside it still leave room for API calls.
	function streamBudget(): number {
		const nav = globalThis.performance?.getEntriesByType?.('navigation')[0] as
			PerformanceNavigationTiming | undefined;
		const proto = nav?.nextHopProtocol ?? '';
		return proto === 'h2' || proto === 'h3' ? 12 : 1;
	}

	const t = untrack(() => target);
	const feed = new LogFeed([], { probe, poll, maxStreams: streamBudget() });

	const stack = createQuery(() => ({
		...stackQuery(t.kind === 'stack' ? t.stackId : ''),
		enabled: t.kind === 'stack'
	}));
	const services = createQuery(() => ({
		...stackServicesQuery(t.kind === 'stack' ? t.stackId : ''),
		enabled: t.kind === 'stack'
	}));

	const sources = $derived.by((): LogSource[] | null => {
		if (t.kind === 'container')
			return [
				{
					key: t.containerId,
					environmentId: t.environmentId,
					containerId: t.containerId,
					label: name
				}
			];
		const env = stack.data?.environmentId;
		if (!env || !services.data) return null;
		return services.data.services.flatMap((svc) => {
			// Every service has the same colour; the prefix names it.
			const color = SERVICE_COLOR;
			return svc.containers
				.filter((c) => c.name)
				.map((c) => ({
					key: c.name!,
					environmentId: env,
					containerId: c.name!,
					service: svc.name,
					label: svc.containers.length > 1 ? c.name! : svc.name,
					color
				}));
		});
	});

	let started = false;
	$effect(() => {
		const s = sources;
		if (!s) return;
		untrack(() => {
			feed.setSources(s);
			if (!started) {
				started = true;
				feed.start();
			}
		});
	});
	onDestroy(() => feed.stop());

	function openWindow() {
		const url =
			t.kind === 'stack'
				? routes.logsWindow({ stackId: t.stackId })
				: routes.logsWindow({ environmentId: t.environmentId, containerId: t.containerId });
		window.open(
			url,
			`docker-manager-logs-${t.kind === 'stack' ? t.stackId : t.containerId}`,
			'popup,width=1100,height=680'
		);
	}

	const loadError = $derived(stack.error ?? services.error);
</script>

{#if t.kind === 'stack' && (stack.isPending || services.isPending)}
	<div class="state" aria-busy="true"><Skeleton lines={6} /></div>
{:else if loadError}
	<div class="state">
		<ErrorState
			bare
			error={loadError}
			title="The services of {name} could not be loaded."
			onretry={() => {
				void stack.refetch();
				void services.refetch();
			}}
		/>
	</div>
{:else if sources && sources.length === 0}
	<div class="state">
		<EmptyState
			icon={ScrollText}
			title="{name} Has No Containers"
			description="Logs appear here once {name} is deployed and its containers run."
		/>
	</div>
{:else}
	<LogViewer
		{feed}
		label="Logs of {name}"
		stackId={t.kind === 'stack' ? t.stackId : undefined}
		downloadName={name.toLowerCase().replace(/[^a-z0-9._-]+/g, '-')}
		onpopout={popout ? openWindow : undefined}
		{dense}
		{extra}
		{service}
	/>
{/if}

<style>
	.state {
		padding: var(--space-5) var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}
</style>
