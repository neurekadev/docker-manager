<script lang="ts">
	// Design system gallery (#22): every token and component with sample
	// data, including a re-composition of the mockup's stack detail from the
	// library. Public on purpose (no API data, no sign-in) so reviewers and
	// feature agents can compare against docs/design/README.md. The "Lazy
	// surfaces" section loads the heavy libraries on demand (#11).
	import { onDestroy } from 'svelte';
	import Clock from '@lucide/svelte/icons/clock';
	import Cpu from '@lucide/svelte/icons/cpu';
	import Download from '@lucide/svelte/icons/download';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import FileText from '@lucide/svelte/icons/file-text';
	import Folder from '@lucide/svelte/icons/folder';
	import Layers from '@lucide/svelte/icons/layers';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import Package from '@lucide/svelte/icons/package';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Rocket from '@lucide/svelte/icons/rocket';
	import Square from '@lucide/svelte/icons/square';
	import SquareTerminal from '@lucide/svelte/icons/square-terminal';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Workflow from '@lucide/svelte/icons/workflow';
	import { ApiRequestError } from '$lib/api/client';
	import { JobWatcher } from '$lib/api/jobs.svelte';
	import {
		DEMO_STACK_ID,
		ScriptedEventSource,
		demoCpuSeries,
		demoJob,
		demoJobClient,
		demoServices,
		playDemoJob,
		type DemoService
	} from '$lib/design/demo';
	import { serviceIdentity, TILE_COLORS, TILE_HEX } from '$lib/design/hue';
	import { serviceIcon } from '$lib/design/icons';
	import { mountLineChart, mountTerminal, mountYamlEditor, type Mounted } from '$lib/lazy';
	import {
		Badge,
		Button,
		Card,
		Checkbox,
		Combobox,
		ConfirmDialog,
		ContextMenu,
		DeniedState,
		DestructiveConfirm,
		Dialog,
		Drawer,
		EmptyState,
		ErrorState,
		IconButton,
		IconTile,
		JobProgress,
		KpiCard,
		Menu,
		Meter,
		Notice,
		OfflineEnvironment,
		PageHeader,
		PasswordField,
		Popover,
		RadioGroup,
		SecretReveal,
		Select,
		Skeleton,
		Sparkline,
		SplitButton,
		StatusBadge,
		StepWizard,
		Switch,
		TabNav,
		Table,
		TextArea,
		TextField,
		Tooltip,
		TriState,
		formatBytes,
		formatPercent,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';

	const tokenGroups: { title: string; tokens: string[] }[] = [
		{
			title: 'Surfaces',
			tokens: [
				'surface-canvas',
				'surface-shell',
				'surface-panel',
				'surface-raised',
				'surface-hover',
				'surface-selected',
				'border-subtle',
				'border-strong'
			]
		},
		{ title: 'Text', tokens: ['text-strong', 'text-default', 'text-muted', 'text-faint'] },
		{
			title: 'Accent and status',
			tokens: [
				'accent',
				'accent-text',
				'ok',
				'ok-soft',
				'warn',
				'warn-soft',
				'danger',
				'danger-soft',
				'info',
				'offline'
			]
		}
	];
	const typeScale = [
		{
			name: 'Page title',
			size: '28 / 34, 600',
			style: 'font-size: 28px; line-height: 34px; font-weight: 600',
			sample: 'Silo'
		},
		{
			name: 'KPI value',
			size: '20 / 28, 600',
			style: 'font-size: 20px; line-height: 28px; font-weight: 600',
			sample: '1.8 GB'
		},
		{
			name: 'Section title',
			size: '16 / 24, 600',
			style: 'font-size: 16px; line-height: 24px; font-weight: 600',
			sample: 'Services'
		},
		{
			name: 'Control',
			size: '14 / 20, 500',
			style: 'font-size: 14px; line-height: 20px; font-weight: 500',
			sample: 'Deploy'
		},
		{
			name: 'Body and tables',
			size: '13 / 20, 400',
			style: 'font-size: 13px; line-height: 20px',
			sample: 'unless-stopped'
		},
		{
			name: 'Caption',
			size: '12 / 16, 400',
			style: 'font-size: 12px; line-height: 16px',
			sample: 'Created 2 weeks ago'
		},
		{
			name: 'Mono',
			size: '12.5 / 20',
			style: 'font-family: var(--font-mono); font-size: 12.5px',
			sample: 'ghcr.io/silo/web:latest'
		}
	];

	const logLines = [
		['silo-web', 'Starting nginx 1.27.3'],
		['silo-api', '[info] Starting API on :8080'],
		['silo-db', 'database system is ready to accept connections'],
		['silo-redis', 'Ready to accept connections tcp'],
		['silo-worker', 'Worker started, pid 1'],
		['silo-api', '[info] GET /health 200 2ms'],
		['silo-web', '192.168.1.23 - - "GET / HTTP/1.1" 200 1532']
	];
	const idOf = (name: string) => {
		const s = demoServices.find((x) => x.name === name);
		return serviceIdentity({ stackId: DEMO_STACK_ID, name, image: s?.image, icon: s?.icon });
	};

	const rowMenu = (s: DemoService): MenuEntry[] => [
		{
			label: `Restart ${s.name}`,
			icon: RefreshCw,
			onSelect: () => toast.success(`Restarted ${s.name}`)
		},
		{
			label: 'View logs',
			icon: FileText,
			onSelect: () => toast.info('Logs open in the Logs tab')
		},
		{ separator: true },
		{ label: 'Stop', icon: Square, tone: 'danger', onSelect: () => (confirmOpen = true) }
	];

	const columns: Column<DemoService>[] = [
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (s) => s.name,
			stack: 'title',
			width: '220px'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (s) => s.status,
			stack: 'status',
			width: '140px'
		},
		{ id: 'containers', header: 'Containers', cell: containersCell, width: '110px' },
		{ id: 'image', header: 'Image', cell: imageCell, sortValue: (s) => s.image, mono: true },
		{ id: 'ports', header: 'Ports', cell: portsCell },
		{ id: 'restart', header: 'Restart policy', cell: restartCell },
		{ id: 'cpu', header: 'CPU', cell: cpuCell, sortValue: (s) => s.cpu, numeric: true },
		{
			id: 'memory',
			header: 'Memory',
			cell: memCell,
			sortValue: (s) => s.memory,
			numeric: true
		},
		{
			id: 'actions',
			header: 'Actions',
			cell: actionsCell,
			hideHeader: true,
			stack: 'actions',
			align: 'end',
			width: '140px'
		}
	];
	let selected = $state<string[]>([]);
	const cpu = demoCpuSeries(40);

	// Overlays and forms.
	let dialogOpen = $state(false);
	let confirmOpen = $state(false);
	let destroyOpen = $state(false);
	let drawerOpen = $state(false);
	let secretOpen = $state(false);
	let name = $state('silo');
	let password = $state('');
	let notes = $state('');
	let policy = $state('missing');
	let registry = $state('');
	let follow = $state(true);
	let backups = $state(true);
	let tri = $state<'inherit' | 'allow' | 'deny'>('inherit');
	let wizardStep = $state(0);

	// Job progress with a scripted stream (partial failure).
	const es = new ScriptedEventSource();
	const watcher = new JobWatcher(demoJob().id, {
		eventSource: () => es,
		client: demoJobClient(demoJob('partial'))
	});
	watcher.start();
	let stopPlay = playDemoJob(es);
	function replay() {
		stopPlay();
		location.reload();
	}

	// Lazy surfaces (#11 proof).
	let editorEl = $state<HTMLElement>();
	let chartEl = $state<HTMLElement>();
	let terminalEl = $state<HTMLElement>();
	let loaded = $state<string[]>([]);
	let contextChoice = $state<string | null>(null);
	const mounted: Mounted[] = [];

	async function load(kind: 'editor' | 'chart' | 'terminal') {
		if (loaded.includes(kind)) return;
		if (kind === 'editor' && editorEl) {
			mounted.push(
				await mountYamlEditor(
					editorEl,
					'services:\n  silo-web:\n    image: nginx\n    ports:\n      - "8080:80"\n',
					{ label: 'compose.yaml' }
				)
			);
		} else if (kind === 'chart' && chartEl) {
			const now = Date.UTC(2026, 8, 25, 10);
			const values = demoCpuSeries(40);
			mounted.push(
				await mountLineChart(
					chartEl,
					'CPU',
					values.map((v, i) => ({ at: new Date(now + i * 60_000), value: v })),
					'%'
				)
			);
		} else if (kind === 'terminal' && terminalEl) {
			const term = await mountTerminal(terminalEl);
			term.write(
				'\x1b[32mroot@silo-api\x1b[0m:/app# ls\r\nDockerfile  package.json  src\r\n'
			);
			mounted.push(term);
		}
		loaded = [...loaded, kind];
	}

	onDestroy(() => {
		stopPlay();
		watcher.stop();
		mounted.forEach((m) => m.destroy());
	});

	const sampleError = new ApiRequestError('homelab is offline', 503, {
		code: 'environment_offline',
		message:
			"Silo can't be deployed because homelab is offline. It will reconnect automatically; try again when it's back",
		requestId: '4f1c2e7a9b0d4c3e8f6a1b2c3d4e5f60',
		retryable: true,
		details: []
	});
</script>

<svelte:head><title>Design system · Docker Manager</title></svelte:head>

{#snippet nameCell(s: DemoService)}
	{@const id = idOf(s.name)}
	<span class="svc">
		<IconTile icon={serviceIcon(id.icon)} color={id.color} size="sm" />
		<span class="svc-text"
			><span class="svc-name">{s.name}</span><span class="svc-desc">{s.description}</span
			></span
		>
	</span>
{/snippet}
{#snippet statusCell(s: DemoService)}<StatusBadge status={s.status} />{/snippet}
{#snippet containersCell(s: DemoService)}<span class="num">{s.running} / {s.desired}</span
	>{/snippet}
{#snippet imageCell(s: DemoService)}{s.image}{/snippet}
{#snippet portsCell(s: DemoService)}
	{#if s.ports.length}
		{#each s.ports as p (p)}<a
				class="mono"
				href="http://192.168.1.10:{p.split(':')[0]}"
				rel="noreferrer"
				target="_blank">{p}</a
			>{/each}
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet restartCell(s: DemoService)}{s.restart}{/snippet}
{#snippet cpuCell(s: DemoService)}{s.running ? formatPercent(s.cpu) : '—'}{/snippet}
{#snippet memCell(s: DemoService)}{s.running ? formatBytes(s.memory) : '—'}{/snippet}
{#snippet actionsCell(s: DemoService)}
	<span class="row-actions">
		<IconButton size="sm" label="Open {s.name}" icon={ExternalLink} />
		<IconButton size="sm" label="Open a terminal in {s.name}" icon={SquareTerminal} />
		<Menu items={rowMenu(s)} label="Actions for {s.name}">
			{#snippet trigger(props)}<IconButton
					{...props}
					size="sm"
					label="More actions for {s.name}"
					icon={EllipsisVertical}
				/>{/snippet}
		</Menu>
	</span>
{/snippet}

<div class="gallery">
	<header class="intro">
		<h1>Docker Manager design system</h1>
		<p class="muted">
			Tokens and components from $lib/design and $lib/ui, with sample data. Rules:
			docs/design/README.md.
		</p>
	</header>

	<section aria-labelledby="stack-title" class="section">
		<h2 id="stack-title">Stack detail, composed from the library</h2>
		<PageHeader
			title="Silo"
			description="Personal cloud and media platform"
			icon={Layers}
			meta={[
				{ icon: Workflow, label: '5 services' },
				{ icon: Package, label: '5 containers' },
				{ icon: Clock, label: 'Created 2 weeks ago' },
				{
					icon: Folder,
					label: 'homelab · silo',
					title: '/var/lib/docker/volumes/docker-manager_stacks/_data/silo'
				}
			]}
		>
			{#snippet status()}<StatusBadge status="running" />{/snippet}
			{#snippet actions()}
				<SplitButton
					label="Deploy"
					icon={Download}
					menuLabel="More deploy options"
					onclick={() => toast.success('Deployed Silo')}
					items={[
						{ label: 'Deploy', onSelect: () => toast.success('Deployed Silo') },
						{
							label: 'Deploy with pull',
							onSelect: () => toast.success('Deployed Silo with fresh images')
						},
						{
							label: 'Build and deploy',
							onSelect: () => toast.success('Built and deployed Silo')
						}
					]}
				/>
				<Button icon={RefreshCw} onclick={() => (confirmOpen = true)}>Restart</Button>
				<Button variant="danger-soft" icon={Square} onclick={() => (destroyOpen = true)}
					>Stop</Button
				>
				<Button icon={RefreshCw}
					>Update <span class="update-dot" aria-hidden="true"></span><span class="sr-only"
						>, update available</span
					></Button
				>
				<Menu
					label="More stack actions"
					items={[
						{ label: 'Down', icon: Square },
						{ label: 'Migrate', icon: Rocket },
						{ label: 'Edit details', icon: FileText },
						{ separator: true },
						{
							label: 'Delete',
							icon: Trash2,
							tone: 'danger',
							onSelect: () => (destroyOpen = true)
						}
					]}
				>
					{#snippet trigger(props)}<IconButton
							{...props}
							variant="secondary"
							label="More stack actions"
							icon={EllipsisVertical}
						/>{/snippet}
				</Menu>
			{/snippet}
		</PageHeader>
		<TabNav
			label="Stack sections"
			current="/design"
			items={[
				{ href: '/design', label: 'Overview' },
				{ href: '/design/files', label: 'Files' },
				{ href: '/design/logs', label: 'Logs' },
				{ href: '/design/terminal', label: 'Terminal' },
				{ href: '/design/revisions', label: 'Revisions' },
				{ href: '/design/policies', label: 'Policies' },
				{ href: '/design/activity', label: 'Activity' }
			]}
		>
			{#snippet after()}<Badge tone="warn" dot>Undeployed changes</Badge>{/snippet}
		</TabNav>
		<div class="kpis">
			<KpiCard
				label="Stack status"
				value="Running"
				tone="ok"
				secondary="All services healthy"
			/>
			<KpiCard
				label="Services"
				value="4 / 5"
				icon={Package}
				color="blue"
				secondary="containers running"
			/>
			<KpiCard label="CPU usage" value="12.4%" icon={Cpu} color="cyan">
				{#snippet sparkline()}<Sparkline
						values={cpu}
						color={TILE_HEX.cyan.fg}
						label="CPU steady around 12% over the last 40 minutes"
					/>{/snippet}
			</KpiCard>
			<KpiCard
				label="Memory usage"
				value="1.8 GB"
				unit="/ 8 GB"
				icon={MemoryStick}
				color="indigo"
			>
				{#snippet bar()}<Meter
						value={1.8}
						max={8}
						label="Memory usage"
						valueText="1.8 GB of 8 GB"
					/>{/snippet}
			</KpiCard>
			<KpiCard
				label="Uptime"
				value="14 days"
				icon={Clock}
				color="green"
				secondary="Since Sep 11, 2026"
			/>
			<KpiCard
				label="Last deploy"
				value="2 days ago"
				icon={Rocket}
				color="violet"
				secondary="Revision a1b2c3d"
			/>
		</div>
		<Card title="Services" padding="none" id="services">
			<Table
				label="Services of Silo"
				rows={demoServices}
				{columns}
				rowKey={(s) => s.name}
				selectable
				bind:selected
				rowLabel={(s) => `Select ${s.name}`}
			/>
		</Card>
		<p class="muted">{selected.length} selected</p>
	</section>

	<section aria-labelledby="hue-title" class="section">
		<h2 id="hue-title">Service hue identity</h2>
		<p class="muted">
			Each service keeps one colour everywhere: its tile, its log prefix, its chart series and
			its filter chip.
		</p>
		<Card>
			<div class="hues">
				{#each demoServices as s (s.name)}
					{@const id = idOf(s.name)}
					<span
						class="chip"
						style="--c: {TILE_HEX[id.color].fg}; --b: {TILE_HEX[id.color].bg}"
					>
						<IconTile icon={serviceIcon(id.icon)} color={id.color} size="sm" />
						{s.name}
					</span>
				{/each}
			</div>
			<pre class="logs" aria-label="Log sample">{#each logLines as [svc, line], i (i)}<span
						class="ts">2026-09-25 10:14:{22 + i}</span
					>  <span style="color: {TILE_HEX[idOf(svc).color].fg}">{svc.padEnd(11)}</span
					> {line}
				{/each}</pre>
		</Card>
	</section>

	<section aria-labelledby="tokens-title" class="section">
		<h2 id="tokens-title">Tokens</h2>
		{#each tokenGroups as g (g.title)}
			<h3>{g.title}</h3>
			<div class="swatches">
				{#each g.tokens as t (t)}
					<div class="swatch">
						<span class="sw" style="background: var(--{t})"></span><code>--{t}</code>
					</div>
				{/each}
			</div>
		{/each}
		<h3>Category tiles</h3>
		<div class="swatches">
			{#each TILE_COLORS as c (c)}
				<div class="swatch"><IconTile icon={Layers} color={c} /><code>{c}</code></div>
			{/each}
		</div>
		<h3>Type</h3>
		<dl class="type">
			{#each typeScale as t (t.name)}
				<div>
					<dt>{t.name} <span class="muted">{t.size}</span></dt>
					<dd style={t.style}>{t.sample}</dd>
				</div>
			{/each}
		</dl>
	</section>

	<section aria-labelledby="actions-title" class="section">
		<h2 id="actions-title">Actions and status</h2>
		<div class="row">
			<Button variant="primary">Save</Button>
			<Button>Restart</Button>
			<Button variant="ghost">Cancel</Button>
			<Button variant="danger">Remove</Button>
			<Button variant="danger-soft" icon={Square}>Stop</Button>
			<Button loading>Deploying</Button>
			<Button size="sm">Small</Button>
			<IconButton label="Open a terminal" icon={SquareTerminal} variant="secondary" />
			<Tooltip text="Tooltips name controls; they never replace the name.">
				{#snippet trigger(props)}<Button {...props}>Hover or focus me</Button>{/snippet}
			</Tooltip>
		</div>
		<div class="row">
			{#each ['running', 'healthy', 'stopped', 'exited', 'paused', 'restarting', 'unhealthy', 'offline', 'queued', 'blocked', 'failed'] as s (s)}
				<StatusBadge status={s} />
			{/each}
			<StatusBadge status="partial" kind="job" />
			<StatusBadge status="running" kind="job" />
			<Badge tone="warn" dot>Update available</Badge>
		</div>
	</section>

	<section aria-labelledby="forms-title" class="section">
		<h2 id="forms-title">Forms</h2>
		<Card>
			<div class="form-grid">
				<TextField
					label="Stack name"
					bind:value={name}
					mono
					description="The Compose project name."
				/>
				<PasswordField
					label="Registry password"
					bind:value={password}
					autocomplete="new-password"
				/>
				<TextField
					label="Invalid example"
					value="silo stack"
					error="Use lowercase letters, digits, dashes and underscores."
				/>
				<Select
					label="Pull policy"
					bind:value={policy}
					options={[
						{ value: 'missing', label: 'Pull missing images' },
						{ value: 'always', label: 'Always pull' }
					]}
				/>
				<Combobox
					label="Registry connection"
					bind:value={registry}
					placeholder="Search connections"
					options={[
						{ value: 'ghcr', label: 'GitHub Container Registry (ghcr.io)' },
						{ value: 'hub', label: 'Docker Hub (docker.io)' },
						{ value: 'local', label: 'Homelab registry (registry.lan:5000)' }
					]}
				/>
				<TextArea label="Notes" bind:value={notes} description="Optional." />
				<Switch
					bind:checked={follow}
					label="Follow"
					description="Scroll with new log lines."
				/>
				<Checkbox
					bind:checked={backups}
					label="Include volumes"
					description="Back up the stack's named volumes too."
				/>
				<RadioGroup
					label="Restart policy"
					value="unless-stopped"
					options={[
						{ value: 'no', label: 'No' },
						{ value: 'unless-stopped', label: 'Unless stopped' },
						{ value: 'always', label: 'Always' }
					]}
				/>
				<TriState
					label="Restart containers on homelab"
					bind:value={tri}
					inherited="allow"
					inheritedFrom="group Operators"
					highRisk
				/>
			</div>
		</Card>
	</section>

	<section aria-labelledby="overlays-title" class="section">
		<h2 id="overlays-title">Overlays and feedback</h2>
		<div class="row">
			<Button onclick={() => (dialogOpen = true)}>Open dialog</Button>
			<Button onclick={() => (confirmOpen = true)}>Confirm dialog</Button>
			<Button variant="danger-soft" onclick={() => (destroyOpen = true)}
				>Destructive confirm</Button
			>
			<Button onclick={() => (drawerOpen = true)}>Open drawer</Button>
			<Popover label="Popover example" align="start">
				{#snippet trigger(props)}<Button {...props}>Popover</Button>{/snippet}
				<p class="pad">Popovers hold non-modal detail, like the notices list.</p>
			</Popover>
			<Button onclick={() => toast.success('Saved compose.yaml')}>Success toast</Button>
			<Button
				onclick={() =>
					toast.error('Silo was not deployed', {
						body: 'homelab is offline. Try again when it is back.'
					})}>Error toast</Button
			>
			<Button onclick={() => (secretOpen = true)}>One-time secret</Button>
		</div>
		<Notice tone="warn" title="silo/compose.yaml changed on disk. Your edits are kept.">
			{#snippet actions()}
				<Button size="sm">Compare</Button>
				<Button size="sm">Reload from disk</Button>
				<Button size="sm">Save as…</Button>
				<Button size="sm" variant="danger-soft">Overwrite</Button>
			{/snippet}
		</Notice>
		<OfflineEnvironment name="edge" since={new Date(Date.now() - 3 * 3600_000).toISOString()} />
	</section>

	<section aria-labelledby="states-title" class="section">
		<h2 id="states-title">States</h2>
		<div class="states">
			<Card
				><EmptyState
					icon={Layers}
					color="blue"
					title="No stacks on homelab yet."
					description="Create a stack or import an existing Compose project."
					level={3}
				>
					{#snippet actions()}<Button variant="primary">Create stack</Button><Button
							>Import project</Button
						>{/snippet}
				</EmptyState></Card
			>
			<Card><DeniedState level={3} /></Card>
			<ErrorState
				error={sampleError}
				title="Silo was not deployed."
				onretry={() => toast.info('Retrying…')}
				compact
			/>
			<Card title="Loading" level={3}><div aria-busy="true"><Skeleton lines={4} /></div></Card
			>
		</div>
	</section>

	<section aria-labelledby="jobs-title" class="section">
		<h2 id="jobs-title">Job progress</h2>
		<div class="jobs">
			<JobProgress {watcher} title="Prune stopped containers on nas" />
			<JobProgress {watcher} title="Prune stopped containers on nas" variant="inline" />
			<Button size="sm" onclick={replay}>Replay</Button>
		</div>
		<Card title="Step wizard" level={3}>
			<StepWizard
				label="Backup setup"
				bind:current={wizardStep}
				steps={[
					{ id: 'repo', label: 'Repository', description: 'Where backups are stored.' },
					{
						id: 'key',
						label: 'Recovery Key',
						description: 'The one key that opens every Docker Manager backup.'
					},
					{ id: 'policy', label: 'Schedule', description: 'When backups run.' }
				]}
				onfinish={() => toast.success('Created the backup policy')}
			>
				{#snippet step(s)}<p class="muted">Content of the “{s.label}” step.</p>{/snippet}
			</StepWizard>
		</Card>
	</section>

	<section aria-labelledby="lazy-title" class="section">
		<h2 id="lazy-title">Lazy surfaces</h2>
		<p data-testid="loaded">Loaded: {loaded.length ? loaded.join(', ') : 'none'}</p>
		<div class="row">
			<Button onclick={() => load('editor')}>Load editor</Button>
			<Button onclick={() => load('chart')}>Load chart</Button>
			<Button onclick={() => load('terminal')}>Load terminal</Button>
		</div>
		<div class="lazy">
			<div class="lazy-box editor" data-testid="editor" bind:this={editorEl}></div>
			<div
				class="lazy-box"
				data-testid="chart"
				bind:this={chartEl}
				style="height: 200px"
			></div>
			<div class="lazy-box" data-testid="terminal" bind:this={terminalEl}></div>
		</div>
		<ContextMenu
			label="File actions"
			items={[
				{ label: 'Rename', onSelect: () => (contextChoice = 'Rename') },
				{ label: 'Download', icon: Download, onSelect: () => (contextChoice = 'Download') },
				{ separator: true },
				{
					label: 'Delete',
					icon: Trash2,
					tone: 'danger',
					onSelect: () => (contextChoice = 'Delete')
				}
			]}
		>
			{#snippet children(props)}
				<div {...props} class="context-target" data-testid="context-target">
					<FileText size={16} strokeWidth={1.75} aria-hidden="true" /> compose.yaml (right-click)
				</div>
			{/snippet}
		</ContextMenu>
		<p data-testid="selected">Selected: {contextChoice ?? 'nothing'}</p>
	</section>
</div>

<Dialog
	bind:open={dialogOpen}
	title="Edit details"
	description="Display metadata of Silo; never written to Compose files."
>
	<TextField label="Description" value="Personal cloud and media platform" />
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (dialogOpen = false)}>Cancel</Button>
		<Button
			variant="primary"
			onclick={() => ((dialogOpen = false), toast.success('Saved details of Silo'))}
			>Save details</Button
		>
	{/snippet}
</Dialog>
<ConfirmDialog
	bind:open={confirmOpen}
	title="Restart Silo?"
	message="Restarts 5 containers of Silo in dependency order."
	confirmLabel="Restart"
	onconfirm={() => toast.success('Restarted Silo')}
/>
<DestructiveConfirm
	bind:open={destroyOpen}
	title="Delete Silo?"
	consequences={[
		'Takes the stack down: removes 5 containers and its network.',
		'Volumes and the project directory are kept.'
	]}
	affected={demoServices.map((s) => ({ label: `silo-${s.name}-1`, detail: 'container' }))}
	confirmText="silo"
	confirmLabel="Delete stack"
	onconfirm={() => toast.success('Deleted Silo')}
/>
<Drawer bind:open={drawerOpen} title="silo-api">
	<div class="pad"><p class="muted">Drawers hold detail panes and the log drawer.</p></div>
</Drawer>
<Dialog bind:open={secretOpen} title="Save your Recovery Key">
	<SecretReveal
		secret="DYRK-7Q2M-XK4P-9WNA-3HJD-L6ZT-RB5E-C8VF-2GUY-QK7S-M4XD-N9PA-H3JT-W6LC"
		label="Recovery Key"
		filename="docker-manager-recovery-key.txt"
		fingerprint="rk_4f1c2e7a9b0d4c3e"
		description="This key opens every Docker Manager backup. Without it, backups cannot be restored."
		onconfirm={() => ((secretOpen = false), toast.success('Recovery Key confirmed'))}
	/>
</Dialog>

<style>
	.gallery {
		display: flex;
		flex-direction: column;
		gap: var(--space-10);
		max-width: 1440px;
		margin: 0 auto;
		padding: var(--space-8) var(--page-gutter) var(--space-12);
	}

	.intro h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
	}

	.section {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.section > h2 {
		font-size: var(--text-section);
		padding-bottom: var(--space-2);
		border-bottom: 1px solid var(--border-subtle);
	}

	h3 {
		margin-top: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-body);
		font-weight: var(--weight-medium);
	}

	.row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.kpis {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
		gap: var(--space-4);
	}

	.svc {
		display: flex;
		align-items: center;
		gap: var(--space-3);
	}

	.svc-text {
		display: flex;
		flex-direction: column;
	}

	.svc-name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.svc-desc {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.row-actions {
		display: inline-flex;
		gap: 2px;
	}

	.update-dot {
		display: inline-block;
		margin-left: 2px;
		vertical-align: middle;
		width: 7px;
		height: 7px;
		border-radius: var(--radius-full);
		background: var(--warn);
	}

	.hues {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
		margin-bottom: var(--space-4);
	}

	.chip {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		padding: 3px 10px 3px 3px;
		border: 1px solid color-mix(in srgb, var(--c) 30%, transparent);
		border-radius: var(--radius-md);
		background: color-mix(in srgb, var(--b) 45%, transparent);
		color: var(--text-strong);
		font-family: var(--font-mono);
		font-size: 12.5px;
	}

	.logs {
		margin: 0;
		padding: var(--space-3) var(--space-4);
		border-radius: var(--radius-md);
		background: var(--code-bg);
		color: var(--text-default);
		font-size: 12.5px;
		line-height: 20px;
		overflow-x: auto;
	}

	.ts {
		color: var(--text-muted);
	}

	.swatches {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
		gap: var(--space-3);
	}

	.swatch {
		display: flex;
		align-items: center;
		gap: var(--space-3);
	}

	.sw {
		width: 40px;
		height: 40px;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
	}

	code {
		color: var(--text-default);
		font-size: 12px;
	}

	.type {
		display: grid;
		gap: var(--space-3);
		margin: 0;
	}

	.type div {
		display: grid;
		grid-template-columns: 220px 1fr;
		align-items: baseline;
		gap: var(--space-4);
	}

	.type dt {
		color: var(--text-default);
	}

	.type dd {
		margin: 0;
		color: var(--text-strong);
	}

	.form-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
		gap: var(--space-5);
		align-items: start;
	}

	.states {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
		gap: var(--space-4);
	}

	.jobs {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;
		align-items: start;
		gap: var(--space-4);
	}

	.lazy {
		display: grid;
		gap: var(--space-3);
	}

	.lazy-box {
		min-height: 40px;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
	}

	.lazy-box.editor :global(.cm-editor) {
		min-height: 120px;
	}

	.context-target {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		width: fit-content;
		padding: var(--space-2) var(--space-3);
		border: 1px dashed var(--border-strong);
		border-radius: var(--radius-md);
	}

	.pad {
		padding: var(--space-4);
	}

	@media (max-width: 767px) {
		.jobs {
			grid-template-columns: 1fr;
		}

		.type div {
			grid-template-columns: 1fr;
			gap: 2px;
		}
	}
</style>
