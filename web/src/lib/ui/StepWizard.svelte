<script lang="ts" module>
	export interface WizardStep {
		id: string;
		label: string;
		description?: string;
	}
</script>

<script lang="ts">
	// Step wizard (#22: setup, backup, restore, migration). A real sequence,
	// so steps are numbered. Next runs `onnext` for the current step (return
	// false or throw to stay; the error is shown); focus moves to the new
	// step's heading so screen-reader users hear where they are.
	import { tick, type Snippet } from 'svelte';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import Check from '@lucide/svelte/icons/check';
	import Button from './Button.svelte';
	import { errorMessage } from './errors';

	interface Props {
		steps: WizardStep[];
		current?: number;
		/** Content of a step. */
		step: Snippet<[WizardStep]>;
		/** Validate or submit the current step; false or a throw keeps it. */
		onnext?: (step: WizardStep) => boolean | void | Promise<boolean | void>;
		onfinish?: () => unknown;
		canAdvance?: boolean;
		/** false hides Back (e.g. once a job the wizard started is running). */
		canGoBack?: boolean;
		nextLabel?: string;
		finishLabel?: string;
		label: string;
	}

	let {
		steps,
		current = $bindable(0),
		step,
		onnext,
		onfinish,
		canAdvance = true,
		canGoBack = true,
		nextLabel = 'Next',
		finishLabel = 'Finish',
		label
	}: Props = $props();

	let busy = $state(false);
	let error = $state<string | null>(null);
	let heading = $state<HTMLElement>();
	const last = $derived(current === steps.length - 1);
	const active = $derived(steps[current]);

	async function go(to: number) {
		current = Math.max(0, Math.min(steps.length - 1, to));
		error = null;
		await tick();
		heading?.focus();
	}

	async function next() {
		busy = true;
		error = null;
		try {
			const ok = await onnext?.(active);
			if (ok === false) return;
			if (last) await onfinish?.();
			else await go(current + 1);
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = false;
		}
	}
</script>

<div class="wizard">
	<ol class="steps" role="list" aria-label="{label} steps">
		{#each steps as s, i (s.id)}
			<li
				class:done={i < current}
				class:current={i === current}
				aria-current={i === current ? 'step' : undefined}
			>
				<span class="marker num" aria-hidden="true">
					{#if i < current}<Check size={14} strokeWidth={2} />{:else}{i + 1}{/if}
				</span>
				<span class="step-label">{s.label}</span>
				<span class="sr-only">{i < current ? '(done)' : ''}</span>
			</li>
		{/each}
	</ol>

	<section class="body" aria-labelledby="wizard-step-title">
		<h2 id="wizard-step-title" tabindex="-1" bind:this={heading}>{active.label}</h2>
		{#if active.description}<p class="desc">{active.description}</p>{/if}
		<div class="content">
			{#key active.id}{@render step(active)}{/key}
		</div>
		{#if error}<p class="error" role="alert">{error}</p>{/if}
	</section>

	<footer class="foot">
		{#if current > 0 && canGoBack}
			<Button variant="ghost" icon={ArrowLeft} disabled={busy} onclick={() => go(current - 1)}
				>Back</Button
			>
		{/if}
		<span class="spacer"></span>
		<Button variant="primary" loading={busy} disabled={!canAdvance} onclick={next}>
			{last ? finishLabel : nextLabel}
		</Button>
	</footer>
</div>

<style>
	.wizard {
		display: flex;
		flex-direction: column;
		gap: var(--space-5);
	}

	.steps {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2) var(--space-5);
		margin: 0;
	}

	.steps li {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--text-muted);
	}

	.marker {
		display: grid;
		place-items: center;
		width: 22px;
		height: 22px;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-full);
		font-size: 11px;
		font-weight: var(--weight-semibold);
	}

	.current {
		color: var(--text-strong);
	}

	.current .marker {
		border-color: var(--accent);
		background: var(--accent);
		color: var(--text-on-accent);
	}

	.done .marker {
		border-color: var(--ok-border);
		background: var(--ok-soft);
		color: var(--ok);
	}

	h2 {
		font-size: var(--text-section);
		line-height: var(--leading-section);
		outline: none;
	}

	.desc {
		margin-top: 2px;
		color: var(--text-muted);
	}

	.content {
		margin-top: var(--space-4);
	}

	.error {
		margin-top: var(--space-3);
		color: var(--danger);
	}

	.foot {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		padding-top: var(--space-4);
		border-top: 1px solid var(--border-subtle);
	}

	.spacer {
		flex: 1;
	}
</style>
