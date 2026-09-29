<script lang="ts">
	// One generated install command (#3): what it does, the shell text with
	// a copy button. The text contains the one-use enrollment token: it lives
	// only in this component's memory and is never stored or logged. Also
	// shows a file to copy (the move page's compose.yaml and .env): `what`
	// names it on the copy button.
	import { CopyButton } from '$lib/ui';

	let {
		title,
		description,
		command,
		what = 'command'
	}: { title: string; description: string; command: string; what?: string } = $props();
</script>

<div class="install">
	<p class="desc">{description}</p>
	<div class="box">
		<pre class="cmd mono" aria-label="{title}: {what}">{command}</pre>
		<div class="copy"><CopyButton value={command} {what} text /></div>
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
		justify-content: flex-end;
	}
</style>
