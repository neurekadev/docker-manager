<script lang="ts">
	// Status badge (#22): dot + text for an API state (running, exited,
	// offline, partial, queued, blocked, ...). `label` overrides the text
	// (e.g. "5 / 5 running"), never the tone.
	import Badge from './Badge.svelte';
	import { statusInfo } from './status';

	interface Props {
		status: string;
		kind?: 'resource' | 'job';
		label?: string;
		title?: string;
	}

	let { status, kind = 'resource', label, title }: Props = $props();
	const info = $derived(statusInfo(status, kind));
</script>

<Badge tone={info.tone} dot pulse={info.pulse} {title}>{label ?? info.label}</Badge>
