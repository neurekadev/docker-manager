<script lang="ts" module>
	export interface MultiSelectOption {
		value: string;
		label: string;
		/** A number after the label (how many items have it). */
		count?: number;
		/** Swatch colour (a token, e.g. var(--danger)). */
		hue?: string;
	}

	export interface MultiSelectGroup {
		/** Heading of the group (omit for a single group). */
		label?: string;
		options: MultiSelectOption[];
	}
</script>

<script lang="ts">
	// MultiSelect (#22; on Popover): several choices from short lists, e.g.
	// the log viewer's levels. The trigger looks like Select and shows
	// `summary`; the popover lists every option as a switch (role="switch",
	// the whole row toggles) with "Only" to keep just that option of its
	// group, "All" per group and "Select all" at the foot. Values are unique
	// across groups; `value` holds the chosen ones.
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import { mergeProps } from 'bits-ui';
	import type { IconComponent } from '$lib/design/icons';
	import Field from './Field.svelte';
	import Popover from './Popover.svelte';

	interface Props {
		label: string;
		groups: MultiSelectGroup[];
		value?: string[];
		/** The trigger's text; default "<allLabel>" or "2 of 6". */
		summary?: string;
		allLabel?: string;
		hideLabel?: boolean;
		icon?: IconComponent;
		/** Tooltip of the trigger (e.g. the filter's name when the label is hidden). */
		title?: string;
		width?: string;
		/** Called with the new value after every change. */
		onchange?: (value: string[]) => void;
	}

	let {
		label,
		groups,
		value = $bindable([]),
		summary,
		allLabel = 'All',
		hideLabel = false,
		icon: Icon,
		title,
		width = '280px',
		onchange
	}: Props = $props();

	let open = $state(false);
	const all = $derived(groups.flatMap((g) => g.options.map((o) => o.value)));
	const everything = $derived(all.every((v) => value.includes(v)));
	const text = $derived(
		summary ??
			(everything
				? allLabel
				: `${all.filter((v) => value.includes(v)).length} of ${all.length}`)
	);

	function set(next: string[]) {
		// Keep the options' order.
		value = all.filter((v) => next.includes(v));
		onchange?.(value);
	}

	function toggle(v: string) {
		set(value.includes(v) ? value.filter((x) => x !== v) : [...value, v]);
	}

	function only(g: MultiSelectGroup, v: string) {
		const mine = g.options.map((o) => o.value);
		set([...value.filter((x) => !mine.includes(x)), v]);
	}

	function groupAll(g: MultiSelectGroup) {
		set([...value, ...g.options.map((o) => o.value)]);
	}
</script>

<Field {label} {hideLabel}>
	{#snippet children(c)}
		<Popover bind:open label={title ?? label} align="start" {width}>
			{#snippet trigger(props)}
				<button
					{...mergeProps(props, { id: c.id })}
					type="button"
					class="dy-input ms-trigger"
					class:narrowed={!everything}
					{title}
				>
					{#if Icon}<Icon
							class="ms-icon"
							size={16}
							strokeWidth={1.75}
							aria-hidden="true"
						/>{/if}
					<span class="ms-value">{text}</span>
					<ChevronDown size={16} strokeWidth={1.75} aria-hidden="true" />
				</button>
			{/snippet}

			<div class="ms-panel">
				{#each groups as g, gi (gi)}
					{@const chosen = g.options.filter((o) => value.includes(o.value)).length}
					<div class="ms-group" role="group" aria-label={g.label ?? label}>
						{#if g.label || groups.length === 1}
							<div class="ms-head">
								<span class="ms-heading">{g.label ?? label}</span>
								{#if chosen < g.options.length}
									<button
										type="button"
										class="ms-link"
										aria-label="All {(g.label ?? label).toLowerCase()}"
										onclick={() => groupAll(g)}>All</button
									>
								{/if}
							</div>
						{/if}
						{#each g.options as o (o.value)}
							{@const on = value.includes(o.value)}
							<div class="ms-row">
								<button
									type="button"
									role="switch"
									aria-checked={on}
									class="ms-option"
									onclick={() => toggle(o.value)}
								>
									<span class="ms-switch" aria-hidden="true"
										><span class="ms-thumb"></span></span
									>
									{#if o.hue}<span
											class="ms-swatch"
											style="--ms-hue: {o.hue}"
											aria-hidden="true"
										></span>{/if}
									<span class="ms-label">{o.label}</span>
									{#if o.count !== undefined}<span class="ms-count num"
											>{o.count}</span
										>{/if}
								</button>
								{#if g.options.length > 1}
									<button
										type="button"
										class="ms-link ms-only"
										aria-label="Only {o.label}"
										onclick={() => only(g, o.value)}>Only</button
									>
								{/if}
							</div>
						{/each}
					</div>
				{/each}
				{#if !everything}
					<div class="ms-foot">
						<button type="button" class="ms-link" onclick={() => set(all)}
							>Select all</button
						>
					</div>
				{/if}
			</div>
		</Popover>
	{/snippet}
</Field>

<style>
	.ms-trigger {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		width: auto;
		max-width: 100%;
		padding-right: 10px;
		text-align: left;
		cursor: pointer;
	}

	.ms-trigger > :global(svg:last-child) {
		flex-shrink: 0;
		color: var(--text-muted);
		transition: transform var(--duration-fast) var(--ease-out);
	}

	.ms-trigger[data-state='open'] > :global(svg:last-child) {
		transform: rotate(180deg);
	}

	.ms-trigger.narrowed {
		border-color: color-mix(in srgb, var(--accent-text) 55%, transparent);
		background: color-mix(in srgb, var(--accent-text) 10%, var(--surface-raised));
	}

	.ms-trigger :global(.ms-icon) {
		flex-shrink: 0;
		color: var(--text-muted);
	}

	.ms-value {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.ms-panel {
		display: grid;
		gap: var(--space-1);
		padding: var(--space-1);
	}

	.ms-group + .ms-group {
		padding-top: var(--space-1);
		border-top: 1px solid var(--border-subtle);
	}

	.ms-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
		padding: 6px var(--space-2) 2px;
	}

	.ms-heading {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		font-weight: var(--weight-medium);
	}

	.ms-row {
		position: relative;
		display: flex;
		align-items: center;
		border-radius: var(--radius-sm);
	}

	.ms-row:hover,
	.ms-row:focus-within {
		background: var(--surface-hover);
	}

	.ms-option {
		display: flex;
		flex: 1;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
		min-height: 32px;
		padding: 6px var(--space-2);
		border: 0;
		border-radius: var(--radius-sm);
		background: none;
		color: var(--text-default);
		font: inherit;
		font-size: var(--text-control);
		line-height: var(--leading-control);
		text-align: left;
		cursor: pointer;
	}

	.ms-option[aria-checked='true'] {
		color: var(--text-strong);
	}

	.ms-option:focus-visible {
		outline: var(--focus-ring);
		outline-offset: -2px;
	}

	/* The Switch's look, small: the row is the control. */
	.ms-switch {
		position: relative;
		flex-shrink: 0;
		width: 26px;
		height: 16px;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-full);
		background: var(--surface-panel);
		transition: background-color var(--duration-fast) var(--ease-out);
	}

	.ms-thumb {
		position: absolute;
		top: 2px;
		left: 2px;
		width: 10px;
		height: 10px;
		border-radius: var(--radius-full);
		background: var(--text-muted);
		transition:
			transform var(--duration-fast) var(--ease-out),
			background-color var(--duration-fast) var(--ease-out);
	}

	[aria-checked='true'] .ms-switch {
		border-color: var(--accent);
		background: var(--accent);
	}

	[aria-checked='true'] .ms-thumb {
		transform: translateX(10px);
		background: var(--text-on-accent);
	}

	.ms-swatch {
		flex-shrink: 0;
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
		background: var(--ms-hue);
	}

	[aria-checked='false'] .ms-swatch {
		background: transparent;
		box-shadow: inset 0 0 0 1.5px var(--ms-hue);
	}

	.ms-label {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.ms-count {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.ms-link {
		flex-shrink: 0;
		padding: 2px var(--space-2);
		border: 0;
		border-radius: var(--radius-sm);
		background: none;
		color: var(--accent-text);
		font: inherit;
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		cursor: pointer;
	}

	.ms-link:hover {
		background: var(--accent-soft);
	}

	.ms-link:focus-visible {
		outline: var(--focus-ring);
		outline-offset: 0;
	}

	/* "Only" shows on the row the pointer or focus is on. */
	.ms-only {
		margin-right: var(--space-1);
		opacity: 0;
	}

	.ms-row:hover .ms-only,
	.ms-row:focus-within .ms-only {
		opacity: 1;
	}

	.ms-foot {
		display: flex;
		justify-content: flex-end;
		padding: var(--space-1) var(--space-1) 0;
		border-top: 1px solid var(--border-subtle);
	}

	@media (pointer: coarse) {
		.ms-option {
			min-height: var(--touch-target);
		}

		.ms-only {
			opacity: 1;
		}
	}
</style>
