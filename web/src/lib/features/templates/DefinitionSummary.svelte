<script lang="ts">
	// What a template version runs (template registry): its services with
	// their image and ports, read from the version's Compose files (default
	// file first, overrides after), and the names of its .env settings.
	// Values are never shown: only whether a setting still needs one. The
	// files come from the version's definition (template.use).
	import { parseYaml } from '$lib/lazy';
	import { Chip, Skeleton, Table, type Column } from '$lib/ui';
	import { composeFiles, composeServices, envKeys, type ServiceSummary } from './model';

	let { files }: { files: readonly { path: string; content: string }[] } = $props();
	const uid = $props.id();

	let services = $state<ServiceSummary[] | null>(null);
	let failed = $state(false);
	const keys = $derived(envKeys(files.find((f) => f.path === '.env')?.content ?? ''));
	const unset = $derived(keys.filter((k) => k.empty).length);

	$effect(() => {
		const compose = composeFiles(files);
		let stale = false;
		services = null;
		failed = false;
		Promise.all(compose.map((f) => parseYaml(f.content)))
			.then((docs) => {
				if (!stale) services = composeServices(docs);
			})
			.catch(() => {
				if (!stale) failed = true;
			});
		return () => {
			stale = true;
		};
	});

	const columns: Column<ServiceSummary>[] = [
		{
			id: 'name',
			header: 'Service',
			sortValue: (s) => s.name,
			maxWidth: '220px',
			truncate: true,
			stack: 'title'
		},
		{
			id: 'image',
			header: 'Image',
			cell: imageCell,
			maxWidth: '360px',
			truncate: true,
			title: (s) => s.image,
			mono: true
		},
		{ id: 'ports', header: 'Ports', cell: portsCell }
	];
</script>

{#snippet imageCell(s: ServiceSummary)}
	{#if s.image}{s.image}{:else if s.built}<span class="muted plain"
			>Built from the template's files</span
		>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet portsCell(s: ServiceSummary)}
	{#if s.ports.length}<span class="mono">{s.ports.join(', ')}</span>{:else}<span class="muted"
			>None published</span
		>{/if}
{/snippet}

<div class="summary">
	{#if failed}
		<p class="muted pad">
			The Compose file could not be read here. Open it in the version's files to see its
			services.
		</p>
	{:else if services === null}
		<div class="pad" aria-busy="true"><Skeleton lines={3} height="20px" /></div>
	{:else}
		<Table label="Services" rows={services} {columns} rowKey={(s) => s.name}>
			{#snippet empty()}<p class="muted pad">
					The Compose file defines no services.
				</p>{/snippet}
		</Table>
	{/if}
	{#if keys.length}
		<section class="env pad" aria-labelledby="{uid}-env-keys">
			<h3 class="subsection-title" id="{uid}-env-keys">Settings in .env</h3>
			{#if unset > 0}
				<p class="muted small">
					{unset}
					{unset === 1 ? 'needs' : 'need'} a value when you create a stack.
				</p>
			{/if}
			<ul class="keys" aria-label="Settings in .env">
				{#each keys as k (k.name)}
					<li>
						<Chip size="sm" label={k.empty ? `${k.name} (no value)` : k.name} />
					</li>
				{/each}
			</ul>
		</section>
	{/if}
</div>

<style>
	.summary {
		display: grid;
		min-width: 0;
	}

	.pad {
		padding: var(--space-4) var(--space-5);
	}

	.plain {
		font-family: var(--font-sans);
	}

	.env {
		display: grid;
		gap: var(--space-2);
		border-top: 1px solid var(--border-subtle);
	}

	.small {
		font-size: var(--text-caption);
	}

	.keys {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}
</style>
