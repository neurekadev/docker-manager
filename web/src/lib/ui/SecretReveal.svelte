<script lang="ts">
	// One-time secret (#22: Recovery Key, API token, recovery codes,
	// invitation link): shown once, with Copy and Download, an optional
	// fingerprint to compare later, and an "I stored it" confirmation that
	// gates Continue. After confirming, the secret is dropped from the page.
	// Never put the secret in a URL, storage or logs.
	import Download from '@lucide/svelte/icons/download';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Button from './Button.svelte';
	import Checkbox from './Checkbox.svelte';
	import CopyButton from './CopyButton.svelte';
	import Notice from './Notice.svelte';

	interface Props {
		/** The secret, or several (recovery codes, one per line). */
		secret: string | string[];
		/** What it is, e.g. "Recovery Key". */
		label: string;
		/** Download file name, e.g. "docker-manager-recovery-key.txt". */
		filename?: string;
		fingerprint?: string;
		/** Why it matters, shown above the secret. */
		description?: string;
		confirmLabel?: string;
		/** The confirmation checkbox; default "I stored the {label} somewhere safe". */
		acknowledgeLabel?: string;
		onconfirm: () => void;
	}

	let {
		secret,
		label,
		filename,
		fingerprint,
		description,
		confirmLabel = 'Continue',
		acknowledgeLabel,
		onconfirm
	}: Props = $props();

	let stored = $state(false);
	let done = $state(false);
	const text = $derived(Array.isArray(secret) ? secret.join('\n') : secret);

	function download() {
		const url = URL.createObjectURL(new Blob([text + '\n'], { type: 'text/plain' }));
		const a = document.createElement('a');
		a.href = url;
		a.download = filename ?? 'docker-manager-secret.txt';
		a.click();
		setTimeout(() => URL.revokeObjectURL(url), 0);
	}

	function confirm() {
		done = true;
		onconfirm();
	}
</script>

{#if !done}
	<div class="reveal">
		<Notice
			tone="warn"
			icon={KeyRound}
			title="Docker Manager shows this {label} only once"
			live="none"
		>
			{description ?? 'Store it somewhere safe before you continue.'}
		</Notice>
		<div class="secret-box">
			<pre class="secret" aria-label={label}>{text}</pre>
			<div class="actions">
				<CopyButton value={text} what={label} text />
				<Button size="sm" icon={Download} onclick={download}>Download</Button>
			</div>
		</div>
		{#if fingerprint}
			<p class="fingerprint">Fingerprint <span class="mono">{fingerprint}</span></p>
		{/if}
		<Checkbox
			bind:checked={stored}
			label={acknowledgeLabel ?? `I stored the ${label} somewhere safe`}
		/>
		<div class="continue">
			<Button variant="primary" disabled={!stored} onclick={confirm}>{confirmLabel}</Button>
		</div>
	</div>
{/if}

<style>
	.reveal {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.secret-box {
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--code-bg);
	}

	.secret {
		margin: 0;
		padding: var(--space-4);
		color: var(--text-strong);
		font-size: 14px;
		line-height: 22px;
		white-space: pre-wrap;
		word-break: break-all;
		user-select: all;
	}

	.actions {
		display: flex;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border-top: 1px solid var(--border-subtle);
	}

	.fingerprint {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.fingerprint .mono {
		color: var(--text-default);
	}

	.continue {
		display: flex;
		justify-content: flex-end;
	}
</style>
