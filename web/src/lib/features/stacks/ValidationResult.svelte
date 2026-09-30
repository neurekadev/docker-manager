<script lang="ts">
	// Validation findings of a Compose definition (#7): errors that block
	// the stack, warnings (obsolete top-level version:, bind sources outside
	// the project directory, unsupported features) and what was understood.
	// Bind sources outside the project directory are one line listing each
	// source once, not also a warning per bind.
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import type { Schema } from '$lib/api/client';

	interface Props {
		validation: Schema<'StackValidation'>;
		/** What the errors block, e.g. "deploying" (the stack editor). */
		fixBefore?: string;
	}

	let { validation, fixBefore = 'creating the stack' }: Props = $props();
	// Per-bind warnings are the line below; other bind_outside_project
	// warnings (a definition file outside the project) stay.
	const warnings = $derived(
		validation.warnings.filter(
			(w) =>
				w.code !== 'bind_outside_project' ||
				!w.service ||
				!validation.binds.some(
					(b) => b.external && b.service === w.service && w.message.includes(b.source)
				)
		)
	);
	const external = $derived([
		...new Set(validation.binds.filter((b) => b.external).map((b) => b.source))
	]);
</script>

<div class="result" role="status">
	{#if validation.valid}
		<p class="ok">
			<CircleCheck size={16} strokeWidth={1.75} aria-hidden="true" />
			Valid: {validation.services.length}
			{validation.services.length === 1 ? 'service' : 'services'}
			({validation.services.map((s) => s.name).join(', ')}).
		</p>
	{:else}
		<p class="bad">
			<CircleAlert size={16} strokeWidth={1.75} aria-hidden="true" />
			The definition has {validation.errors.length}
			{validation.errors.length === 1 ? 'error' : 'errors'}; fix {validation.errors.length ===
			1
				? 'it'
				: 'them'} before {fixBefore}.
		</p>
	{/if}
	{#if validation.errors.length}
		<ul class="issues errors" aria-label="Errors">
			{#each validation.errors as e, i (i)}
				<li>
					<span class="code mono">{e.code}</span>
					{#if e.service}<span class="svc mono">{e.service}</span>{/if}
					{e.message}
				</li>
			{/each}
		</ul>
	{/if}
	{#if warnings.length}
		<p class="warn-head">
			<TriangleAlert size={16} strokeWidth={1.75} aria-hidden="true" />
			{warnings.length}
			{warnings.length === 1 ? 'warning' : 'warnings'}
		</p>
		<ul class="issues warnings" aria-label="Warnings">
			{#each warnings as w, i (i)}
				<li>
					<span class="code mono">{w.code}</span>
					{#if w.service}<span class="svc mono">{w.service}</span>{/if}
					{w.message}
				</li>
			{/each}
		</ul>
	{/if}
	{#if external.length}
		<p class="note">
			Bind sources outside the project directory are not included in stack backups unless a
			backup policy opts them in:
			{#each external as source, i (source)}<span class="mono">{source}</span>{i <
				external.length - 1
					? ', '
					: ''}{/each}.
		</p>
	{/if}
</div>

<style>
	.result {
		display: grid;
		gap: var(--space-2);
	}

	.ok,
	.bad,
	.warn-head {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		font-weight: var(--weight-medium);
	}

	.ok {
		color: var(--ok);
	}

	.bad {
		color: var(--danger);
	}

	.warn-head {
		color: var(--warn);
	}

	.issues {
		display: grid;
		gap: 4px;
		margin: 0;
		padding-left: 18px;
		color: var(--text-default);
	}

	.code {
		margin-right: var(--space-2);
		color: var(--text-muted);
	}

	.svc {
		margin-right: var(--space-2);
		color: var(--text-strong);
	}

	.note {
		color: var(--text-muted);
	}
</style>
