<script lang="ts">
	// One generated install command (#3): what it does, the shell text with
	// a copy button. The text contains the one-use enrollment token: it lives
	// only in this component's memory and is never stored or logged. Also
	// shows a file to copy (the move page's compose.yaml and .env): `what`
	// names it on the copy button, and `filename` adds a download button.
	import Download from '@lucide/svelte/icons/download';
	import { Button, CopyButton } from '$lib/ui';

	let {
		title,
		description,
		command,
		what = 'command',
		filename
	}: {
		title: string;
		description: string;
		command: string;
		what?: string;
		/** Offers the text as a file with this name. */
		filename?: string;
	} = $props();

	function download() {
		if (!filename) return;
		const text = command.endsWith('\n') ? command : `${command}\n`;
		const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
		const a = document.createElement('a');
		a.href = url;
		a.download = filename;
		document.body.appendChild(a);
		a.click();
		a.remove();
		setTimeout(() => URL.revokeObjectURL(url), 0);
	}
</script>

<div class="install">
	{#if description}<p class="desc">{description}</p>{/if}
	<div class="box">
		<pre class="cmd mono" aria-label="{title}: {what}">{command}</pre>
		<div class="copy">
			{#if filename}
				<Button size="sm" icon={Download} onclick={download}>Download {what}</Button>
			{/if}
			<CopyButton value={command} {what} text />
		</div>
	</div>
</div>

<style>
	.install {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		min-width: 0;
	}

	.desc {
		max-width: 80ch;
		color: var(--text-muted);
	}

	.box {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
	}

	.cmd {
		margin: 0;
		overflow-x: auto;
		color: var(--text-default);
		white-space: pre;
	}

	.copy {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: var(--space-2);
	}
</style>
