<script lang="ts">
	// Tag input (#22): a list of short values (template tags, network
	// aliases) as removable chips in a field box, followed by a text input.
	// Space, Enter, a comma or Tab ends a tag; pasted text splits at commas
	// and whitespace; Backspace in the empty input removes the last tag;
	// leaving the field keeps what was typed. Duplicates are ignored, at
	// most `max` fit. `validate` explains a refused tag (the field's error);
	// refused tags stay as danger-outlined chips so they can be removed.
	// The chips are a list named "Current <label>". Pure helpers: taginput.ts.
	import Chip from './Chip.svelte';
	import Field from './Field.svelte';
	import { TAG_SEPARATOR, addTags, isTagKey, splitTagText } from './taginput';

	interface Props {
		label: string;
		values?: string[];
		/** Turns typed text into a tag (default: trim). */
		normalize?: (raw: string) => string;
		/** Why a tag is refused ('' when valid). */
		validate?: (tag: string) => string;
		/** Most tags. */
		max?: number;
		description?: string;
		/** An explanation behind an (i) after the label. */
		info?: string;
		/** Shows a muted "Optional" after the label. */
		optional?: boolean;
		error?: string | null;
		hideLabel?: boolean;
		disabled?: boolean;
		placeholder?: string;
		/** Submits the tags comma-separated in a hidden input of this name. */
		name?: string;
		id?: string;
	}

	let {
		label,
		values = $bindable([]),
		normalize = (s: string) => s.trim(),
		validate,
		max = Infinity,
		info,
		optional = false,
		description,
		error,
		hideLabel = false,
		disabled = false,
		placeholder,
		name,
		id
	}: Props = $props();

	let text = $state('');
	let input = $state<HTMLInputElement | null>(null);

	const problems = $derived(validate ? values.map((v) => validate(v)) : []);
	const full = $derived(values.length >= max);
	const shownError = $derived(
		error ||
			problems.find(Boolean) ||
			(full && text.trim() ? `At most ${max} fit. Remove one to add another.` : '') ||
			null
	);

	/** Adds the tags in `raw`; what does not fit stays in the input. */
	function commit(raw: string) {
		const r = addTags(values, splitTagText(raw), normalize, max);
		if (r.values.length !== values.length) values = r.values;
		text = r.overflow.join(' ');
	}

	function remove(i: number) {
		values = values.filter((_, j) => j !== i);
		input?.focus();
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.isComposing) return;
		if (isTagKey(e.key)) {
			// Enter in an empty input still submits the form.
			if (e.key === 'Enter' && !text.trim()) return;
			e.preventDefault();
			if (text.trim()) commit(text);
		} else if (e.key === 'Tab') {
			if (text.trim()) commit(text);
		} else if (e.key === 'Backspace' && text === '' && values.length) {
			e.preventDefault();
			values = values.slice(0, -1);
		}
	}

	// Phone keyboards do not report Space or a comma as keys: end the tags
	// before the last separator here, keep the rest as the text.
	function oninput() {
		if (!TAG_SEPARATOR.test(text)) return;
		const parts = text.split(TAG_SEPARATOR);
		const last = parts.pop() ?? '';
		const r = addTags(values, parts.filter(Boolean), normalize, max);
		if (r.values.length !== values.length) values = r.values;
		text = [...r.overflow, last].join(' ');
	}

	function onpaste(e: ClipboardEvent) {
		const pasted = e.clipboardData?.getData('text/plain') ?? '';
		if (!TAG_SEPARATOR.test(pasted)) return;
		e.preventDefault();
		const el = e.currentTarget as HTMLInputElement;
		const start = el.selectionStart ?? text.length;
		const end = el.selectionEnd ?? text.length;
		commit(text.slice(0, start) + pasted + text.slice(end));
	}
</script>

<Field {label} {description} {info} {optional} error={shownError} {hideLabel} {id}>
	{#snippet children(c)}
		<!-- A click anywhere in the box focuses the input (the input stays the keyboard target). -->
		<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
		<div
			class="dy-input box"
			class:invalid={c.invalid}
			class:disabled
			onclick={(e) => {
				if (!(e.target as Element).closest('button')) input?.focus();
			}}
		>
			{#if values.length}
				<ul class="tags" aria-label="Current {label}">
					{#each values as tag, i (`${i}:${tag}`)}
						<li>
							<Chip
								label={tag}
								size="sm"
								invalid={!!problems[i]}
								title={problems[i] || undefined}
								{disabled}
								onremove={() => remove(i)}
							/>
						</li>
					{/each}
				</ul>
			{/if}
			<input
				bind:this={input}
				bind:value={text}
				id={c.id}
				class="entry"
				type="text"
				autocomplete="off"
				autocapitalize="off"
				spellcheck="false"
				aria-describedby={c.describedBy}
				aria-invalid={c.invalid || undefined}
				placeholder={values.length ? undefined : placeholder}
				{disabled}
				{onkeydown}
				{oninput}
				{onpaste}
				onblur={() => {
					if (text.trim()) commit(text);
				}}
			/>
			{#if name}<input type="hidden" {name} value={values.join(',')} />{/if}
		</div>
	{/snippet}
</Field>

<style>
	.box {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1);
		height: auto;
		min-height: var(--control-height);
		padding: 3px var(--space-2);
		cursor: text;
	}

	.box:has(.entry:focus-visible) {
		outline: var(--focus-ring);
		outline-offset: 0;
		border-color: var(--accent-text);
	}

	.box.invalid {
		border-color: var(--danger);
	}

	.box.disabled {
		opacity: 0.55;
		cursor: not-allowed;
	}

	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1);
		min-width: 0;
		max-width: 100%;
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.tags li {
		min-width: 0;
		max-width: 100%;
	}

	.entry {
		flex: 1 1 6rem;
		min-width: 6rem;
		height: 26px;
		padding: 0 var(--space-1);
		border: 0;
		background: transparent;
		color: var(--text-strong);
		font: inherit;
		font-size: var(--text-control);
		line-height: var(--leading-control);
		outline: none;
	}

	.entry::placeholder {
		color: var(--text-muted);
	}

	.entry:disabled {
		cursor: not-allowed;
	}
</style>
