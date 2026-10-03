<script lang="ts">
	// Diagnostics (#34): the owner's support bundle (versions, redacted
	// configuration, support-matrix checks, agent status, audit chain
	// verification, recent logs; never secrets) and the internal metrics
	// endpoint for Prometheus.
	import { createQuery } from '@tanstack/svelte-query';
	import Download from '@lucide/svelte/icons/download';
	import { api } from '$lib/api/client';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { Badge, Button, Card, CopyButton, DeniedState, Notice } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';

	usePage({
		title: 'Diagnostics',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'Diagnostics' }]
	});

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const metricsUrl = `${globalThis.location?.origin ?? ''}/api/v1/system/metrics`;

	// Whether the endpoint is on (404 while DOCKER_MANAGER_METRICS_ENABLED is off).
	const metrics = createQuery(() => ({
		queryKey: ['settings', 'item', 'metrics-endpoint'],
		queryFn: async ({ signal }: { signal: AbortSignal }) => {
			const { response } = await api.GET('/api/v1/system/metrics', {
				parseAs: 'text',
				signal
			});
			return response.status;
		},
		enabled: can(access, 'system.metrics.read'),
		retry: false,
		staleTime: 60_000
	}));
</script>

<Page>
	<SettingsHeader title="Diagnostics" />
	{#if perms.data && !access.owner && !can(access, 'system.metrics.read')}
		<DeniedState level={2} title="Diagnostics are for the owner." />
	{:else}
		{#if access.owner}
			<Card title="Support Bundle" subtitle="A zip to attach to a bug report.">
				<div class="inside">
					<Disclosure summary="What’s Inside">
						<ul class="plain" role="list">
							<li>
								Versions of the manager, the API, the agent protocol and every agent
							</li>
							<li>The effective configuration, redacted</li>
							<li>
								Support-matrix checks per environment and agent connection status
							</li>
							<li>
								Audit chain verification, job queue summary, database and snapshot
								status
							</li>
							<li>The manager's recent log lines</li>
						</ul>
					</Disclosure>
				</div>
				<Notice tone="info" title="No Secrets Inside" live="none">
					Secrets, credentials, the Recovery Key, Compose and .env contents and job inputs
					are never included. Downloads are recorded in the audit log.
				</Notice>
				<div class="act">
					<Button variant="primary" icon={Download} href="/api/v1/support-bundle"
						>Download Support Bundle</Button
					>
				</div>
			</Card>
		{/if}
		<Card
			title="Internal Metrics"
			subtitle="Docker Manager's own metrics in Prometheus format."
		>
			<p class="line">
				<span class="mono url">{metricsUrl}</span>
				<CopyButton value={metricsUrl} what="metrics URL" />
				{#if metrics.data === 200}<Badge tone="ok" dot>On</Badge>
				{:else if metrics.data === 404}<Badge dot>Off</Badge>{/if}
			</p>
			<ol class="steps" role="list">
				<li>
					Start the manager with <span class="mono"
						>DOCKER_MANAGER_METRICS_ENABLED=true</span
					> (off by default).
				</li>
				<li>Create an API token that may only “Scrape Internal Metrics”.</li>
				<li>
					Scrape the URL with <span class="mono">Authorization: Bearer &lt;token&gt;</span
					>.
				</li>
			</ol>
			<div class="act">
				<Button href={routes.apiTokenNew()}>Create a Token</Button>
			</div>
		</Card>
	{/if}
</Page>

<style>
	.inside {
		margin-bottom: var(--space-4);
	}

	.plain {
		display: grid;
		gap: var(--space-1);
		padding-left: var(--space-5);
		list-style: disc;
	}

	.steps {
		display: grid;
		gap: var(--space-1);
		margin: var(--space-3) 0;
		padding-left: var(--space-5);
		list-style: decimal;
	}

	.line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.url {
		overflow-wrap: anywhere;
	}

	.act {
		margin-top: var(--space-4);
	}
</style>
