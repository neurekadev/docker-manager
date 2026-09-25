<script lang="ts">
	// Environments a list could not read (#6: an offline host yields a clear
	// error, not a silently shorter list). One selected environment that is
	// offline gets the full offline banner; several get one notice each.
	import WifiOff from '@lucide/svelte/icons/wifi-off';
	import type { Unavailable } from '$lib/api/multi-env';
	import { Notice, OfflineEnvironment, errorMessage } from '$lib/ui';

	interface Props {
		unavailable: Unavailable[];
		/** What the list shows, e.g. "containers". */
		what: string;
		/** One environment is selected (show the full banner). */
		single: boolean;
	}

	let { unavailable, what, single }: Props = $props();
	const offline = $derived(unavailable.filter((u) => u.offline));
	const failed = $derived(unavailable.filter((u) => !u.offline));
</script>

{#if single && offline.length === 1}
	<OfflineEnvironment
		name={offline[0].environment.name}
		since={offline[0].environment.connectionChangedAt}
	/>
{:else if offline.length}
	<Notice
		tone="offline"
		icon={WifiOff}
		title="{offline.map((u) => u.environment.name).join(', ')} {offline.length === 1
			? 'is'
			: 'are'} offline"
	>
		Their {what} are not listed until their agents reconnect.
	</Notice>
{/if}
{#each failed as u (u.environment.id)}
	<Notice tone="warn" title="{u.environment.name} could not be read">
		{errorMessage(u.error)} The {what} of the other environments are listed.
	</Notice>
{/each}
