<script lang="ts">
	// Free text with suggestions (#22): a text field whose themed listbox
	// offers matching known values (e.g. existing volume names) while any
	// text stays allowed (a new volume). The ARIA combobox pattern: arrow
	// keys move through the suggestions, Enter takes one, Escape closes the
	// list; a pointer picks one. Replaces the browser's <datalist> popup.
	import Field from './Field.svelte';

	interface Props {
		label: string;
		value?: string;
		/** Known values offered while they match the text (case-insensitive). */
		suggestions: readonly string[];
		description?: string;
		/** An explanation behind an (i) after the label. */
		info?: string;
		/** Shows a muted "Optional" after the label. */
		optional?: boolean;
		error?: string | null;
		hideLabel?: boolean;
		placeholder?: string;
		required?: boolean;
		mono?: boolean;
		/** Most suggestions shown at once. */
		max?: number;
	}

	let {
		label,
		value = $bindable(''),
		suggestions,
		info,
		optional = false,
		description,
		error,
		hideLabel = false,
		placeholder,
		required = false,
		mono = false,
		max = 8
	}: Props = $props();
	const uid = $props.id();
	const listId = `sg-${uid}`;

	let open = $state(false);
	let active = $state(-1);
	const matches = $derived.by(() => {
		const q = value.trim().toLowerCase();
		return suggestions.filter((s) => s !== value && s.toLowerCase().includes(q)).slice(0, max);
	});
	const shown = $derived(open && matches.length > 0);

	function pick(s: string) {
		value = s;
		open = false;
		active = -1;
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			open = true;
			active = matches.length ? (active + 1) % matches.length : -1;
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			open = true;
			active = matches.length ? (active <= 0 ? matches.length : active) - 1 : -1;
		} else if (e.key === 'Enter' && shown && active >= 0) {
			e.preventDefault();
			pick(matches[active]);
		} else if (e.key === 'Escape' && shown) {
			e.preventDefault();
			open = false;
			active = -1;
		}
	}
</script>

<Field {label} {description} {info} {optional} {error} {hideLabel} {required}>
	{#snippet children(c)}
		<div class="wrap">
			<input
				bind:value
				id={c.id}
				class="dy-input"
				class:mono
				type="text"
				role="combobox"
				autocomplete="off"
				spellcheck="false"
				aria-autocomplete="list"
				aria-expanded={shown}
				aria-controls={listId}
				aria-activedescendant={shown && active >= 0 ? `${listId}-${active}` : undefined}
				aria-describedby={c.describedBy}
				aria-invalid={c.invalid || undefined}
				{placeholder}
				{required}
				oninput={() => {
					open = true;
					active = -1;
				}}
				onfocus={() => (open = true)}
				onblur={() => {
					open = false;
					active = -1;
				}}
				{onkeydown}
			/>
			<ul id={listId} role="listbox" aria-label="Suggestions for {label}" hidden={!shown}>
				{#each matches as s, i (s)}
					<li
						id="{listId}-{i}"
						role="option"
						aria-selected={i === active}
						class:mono
						onpointerdown={(e) => {
							// Keep focus in the input; take the value on press.
							e.preventDefault();
							pick(s);
						}}
					>
						{s}
					</li>
				{/each}
			</ul>
		</div>
	{/snippet}
</Field>

<style>
	.wrap {
		position: relative;
	}

	ul {
		position: absolute;
		top: calc(100% + 6px);
		left: 0;
		right: 0;
		z-index: var(--z-menu);
		max-height: 280px;
		margin: 0;
		overflow-y: auto;
		padding: var(--space-1);
		list-style: none;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
	}

	ul[hidden] {
		display: none;
	}

	li {
		min-height: 32px;
		padding: 6px var(--space-2);
		overflow: hidden;
		border-radius: var(--radius-sm);
		color: var(--text-default);
		font-size: var(--text-control);
		line-height: var(--leading-control);
		text-overflow: ellipsis;
		white-space: nowrap;
		cursor: pointer;
	}

	li.mono {
		font-family: var(--font-mono);
		font-size: 13px;
	}

	li:hover,
	li[aria-selected='true'] {
		background: var(--surface-hover);
		color: var(--text-strong);
	}
</style>
