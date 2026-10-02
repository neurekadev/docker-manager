<script lang="ts">
	// This instance's registry (template registry; "Share Your Templates" on
	// the Template sources page): the URL other instances add to use its
	// public templates, how many templates it
	// shares and a link to its public page. The URL is this manager's
	// own origin (DOCKER_MANAGER_PUBLIC_URL).
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import { routes } from '$lib/routes';
	import { Button, Card, CopyButton } from '$lib/ui';
	import type { Template } from './queries';

	let { templates }: { templates: readonly Template[] } = $props();
	const url = typeof location !== 'undefined' ? location.origin : '';
	const shared = $derived(templates.filter((t) => t.visibility === 'public' && t.latest).length);
</script>

<Card title="Share Your Templates" id="own-registry">
	<div class="body">
		<p class="muted">
			{shared === 0
				? 'No template is shared yet: make a template public and publish a version to share it.'
				: `Shares ${shared} public ${shared === 1 ? 'template' : 'templates'}.`}
			Other Docker Managers add this address as a template source to browse and use them.
		</p>
		<div class="row">
			<div class="url">
				<code>{url}</code>
				<CopyButton value={url} what="address" />
			</div>
			<Button icon={ExternalLink} href={routes.registry()}>Open the Public Page</Button>
		</div>
	</div>
</Card>

<style>
	.body {
		display: grid;
		gap: var(--space-3);
	}

	.row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.url {
		display: flex;
		flex: 1 1 280px;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
	}

	code {
		flex: 1;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
