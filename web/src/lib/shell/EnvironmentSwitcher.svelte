<script lang="ts">
	// Environment switcher (#22): one environment or "All environments",
	// remembered per user (environment.svelte.ts). The trigger shows the
	// status dot, the name and the Engine version; the list has a filter,
	// each environment's status in words, and "Add environment" for users
	// who may enroll agents. `compact` is the icon-rail variant (the
	// switcher's up-down caret with the status dot, not the Environments
	// nav icon).
	import Check from '@lucide/svelte/icons/check';
	import ChevronsUpDown from '@lucide/svelte/icons/chevrons-up-down';
	import Plus from '@lucide/svelte/icons/plus';
	import Search from '@lucide/svelte/icons/search';
	import { mergeProps } from 'bits-ui';
	import type { Environment } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import Popover from '$lib/ui/Popover.svelte';
	import Tooltip from '$lib/ui/Tooltip.svelte';

	interface Props {
		environments: Environment[];
		/** Selected environment ID; null = All environments. */
		selected: string | null;
		onselect: (id: string | null) => void;
		/** Engine version of the selected environment, when known. */
		engineVersion?: string | null;
		canAdd?: boolean;
		compact?: boolean;
		loading?: boolean;
	}

	let {
		environments,
		selected,
		onselect,
		engineVersion,
		canAdd = false,
		compact = false,
		loading = false
	}: Props = $props();

	let open = $state(false);
	let filter = $state('');
	let listEl = $state<HTMLElement>();

	const current = $derived(environments.find((e) => e.id === selected) ?? null);
	const online = $derived(environments.filter((e) => e.online).length);
	const shown = $derived(
		filter
			? environments.filter((e) => e.name.toLowerCase().includes(filter.toLowerCase()))
			: environments
	);
	const title = $derived(current ? current.name : 'All Environments');
	const secondary = $derived(
		current
			? current.online
				? engineVersion
					? `Docker ${engineVersion}`
					: 'Online'
				: 'Offline'
			: loading
				? 'Loading…'
				: `${online} of ${environments.length} online`
	);
	const tone = $derived(
		current ? (current.online ? 'ok' : 'offline') : online < environments.length ? 'warn' : 'ok'
	);

	function choose(id: string | null) {
		onselect(id);
		open = false;
		filter = '';
	}

	function onListKey(e: KeyboardEvent) {
		if (!listEl || (e.key !== 'ArrowDown' && e.key !== 'ArrowUp')) return;
		const opts = [...listEl.querySelectorAll<HTMLElement>('[role="option"]')];
		const i = opts.indexOf(document.activeElement as HTMLElement);
		const next = e.key === 'ArrowDown' ? Math.min(opts.length - 1, i + 1) : Math.max(0, i - 1);
		opts[next]?.focus();
		e.preventDefault();
	}
</script>

{#snippet dot(t: string)}
	<span class="dot {t}" aria-hidden="true"></span>
{/snippet}

<Popover
	bind:open
	label="Choose an Environment"
	align="start"
	side={compact ? 'right' : 'bottom'}
	width="300px"
>
	{#snippet trigger(props)}
		{#if compact}
			<Tooltip text="{title}: {secondary}" side="right">
				{#snippet trigger(tp)}
					<button
						{...mergeProps(props, tp)}
						type="button"
						class="compact"
						aria-label="Environment: {title}, {secondary}"
					>
						<ChevronsUpDown size={18} strokeWidth={1.75} aria-hidden="true" />
						{@render dot(tone)}
					</button>
				{/snippet}
			</Tooltip>
		{:else}
			<button
				{...props}
				type="button"
				class="switcher"
				aria-label="Environment: {title}, {secondary}"
			>
				{@render dot(tone)}
				<span class="text">
					<span class="name">{title}</span>
					<span class="secondary">{secondary}</span>
				</span>
				<ChevronsUpDown size={16} strokeWidth={1.75} aria-hidden="true" class="caret" />
			</button>
		{/if}
	{/snippet}

	<div class="panel">
		{#if environments.length > 5}
			<label class="filter">
				<Search size={16} strokeWidth={1.75} aria-hidden="true" />
				<span class="sr-only">Filter Environments</span>
				<input bind:value={filter} placeholder="Filter environments" autocomplete="off" />
			</label>
		{/if}
		<div
			class="list"
			role="listbox"
			aria-label="Environments"
			tabindex="-1"
			bind:this={listEl}
			onkeydown={onListKey}
		>
			<button
				type="button"
				role="option"
				aria-selected={selected === null}
				class="option"
				onclick={() => choose(null)}
			>
				{@render dot(online < environments.length ? 'warn' : 'ok')}
				<span class="option-text">
					<span class="name">All Environments</span>
					<span class="secondary">{online} of {environments.length} online</span>
				</span>
				{#if selected === null}<Check
						size={16}
						strokeWidth={1.75}
						aria-hidden="true"
					/>{/if}
			</button>
			{#each shown as env (env.id)}
				<button
					type="button"
					role="option"
					aria-selected={selected === env.id}
					class="option"
					onclick={() => choose(env.id)}
				>
					{@render dot(env.online ? 'ok' : 'offline')}
					<span class="option-text">
						<span class="name">{env.name}</span>
						<span class="secondary">{env.online ? 'Online' : 'Offline'}</span>
					</span>
					{#if selected === env.id}<Check
							size={16}
							strokeWidth={1.75}
							aria-hidden="true"
						/>{/if}
				</button>
			{:else}
				{#if environments.length === 0}
					<p class="none">No environments yet.</p>
				{:else}
					<p class="none">No environment matches “{filter}”.</p>
				{/if}
			{/each}
		</div>
		{#if canAdd}
			<a class="add" href={routes.addEnvironment()} onclick={() => (open = false)}>
				<Plus size={16} strokeWidth={1.75} aria-hidden="true" />
				Add Environment
			</a>
		{/if}
	</div>
</Popover>

<style>
	.switcher {
		display: flex;
		align-items: center;
		gap: 10px;
		width: 100%;
		min-height: 48px;
		padding: 6px 10px;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-panel);
		text-align: left;
		transition: background-color var(--duration-fast) var(--ease-out);
	}

	.switcher:hover,
	.switcher[data-state='open'] {
		background: var(--surface-hover);
	}

	.switcher :global(.caret) {
		color: var(--text-muted);
		margin-left: auto;
	}

	.compact {
		position: relative;
		display: grid;
		place-items: center;
		width: 40px;
		height: 40px;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-panel);
		color: var(--text-default);
	}

	.compact .dot {
		position: absolute;
		top: 6px;
		right: 6px;
	}

	.text,
	.option-text {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
	}

	.name {
		color: var(--text-strong);
		font-size: var(--text-body);
		font-weight: var(--weight-medium);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.secondary {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dot {
		flex-shrink: 0;
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
	}

	.dot.ok {
		background: var(--ok);
		box-shadow: 0 0 0 3px var(--ok-soft);
	}

	.dot.warn {
		background: var(--warn);
		box-shadow: 0 0 0 3px var(--warn-soft);
	}

	.dot.offline {
		background: var(--offline);
		box-shadow: 0 0 0 3px var(--offline-soft);
	}

	.panel {
		display: flex;
		flex-direction: column;
		padding: var(--space-1);
	}

	.filter {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin: var(--space-1) var(--space-1) var(--space-2);
		padding: 0 var(--space-2);
		height: 32px;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-sm);
		background: var(--surface-panel);
		color: var(--text-muted);
	}

	/* The input draws no ring of its own: the whole filter shows focus. */
	.filter:focus-within {
		outline: var(--focus-ring);
		outline-offset: 0;
		border-color: var(--accent-text);
	}

	.filter input {
		flex: 1;
		min-width: 0;
		border: 0;
		background: none;
		color: var(--text-strong);
		outline: none;
	}

	.list {
		display: flex;
		flex-direction: column;
		gap: 2px;
		max-height: 320px;
		overflow-y: auto;
		outline: none;
	}

	.option {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px var(--space-2);
		border: 0;
		border-radius: var(--radius-sm);
		background: none;
		text-align: left;
		color: var(--accent-text);
	}

	.option:hover,
	.option:focus-visible {
		background: var(--surface-hover);
	}

	.option[aria-selected='true'] {
		background: var(--surface-selected);
	}

	.none {
		padding: var(--space-2);
		color: var(--text-muted);
	}

	.add {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin-top: var(--space-1);
		padding: 8px var(--space-2);
		border-top: 1px solid var(--border-subtle);
		color: var(--accent-text);
	}
</style>
