<script lang="ts" module>
	/** The words and tone of an image's update state (#20); null: nothing to show. */
	export function updateBadge(
		status: string | undefined
	): { tone: 'warn' | 'ok' | 'danger' | 'muted'; text: string } | null {
		switch (status) {
			case 'update_available':
				return { tone: 'warn', text: 'Update available' };
			case 'up_to_date':
				return { tone: 'ok', text: 'Up to date' };
			case 'check_failed':
				return { tone: 'danger', text: 'The last update check failed' };
			case 'run_failed':
				return { tone: 'danger', text: 'The last update failed' };
			case 'quarantined':
				return { tone: 'warn', text: 'The new image is quarantined after a failed update' };
			case 'unchecked':
				return { tone: 'muted', text: 'Not checked for updates yet' };
			default:
				return null;
		}
	}
</script>

<script lang="ts">
	// An image's update state as an icon next to the image (#20, #22): the
	// state in its tooltip and accessible name. With a policy the user may
	// check, the icon is a button that checks that policy's images again,
	// spinning while the check runs: one started in this tab (updateChecks,
	// shared by every badge of the policy) or any running update.check of the
	// policy in the running jobs list (after a reload, or started elsewhere;
	// one shared query). Nothing shows without an update state.
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import CircleArrowUp from '@lucide/svelte/icons/circle-arrow-up';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleDashed from '@lucide/svelte/icons/circle-dashed';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import { createQuery, useQueryClient, type QueryClient } from '@tanstack/svelte-query';
	import { activeJobsQuery } from '$lib/api/queries';
	import { checkForUpdates, updateChecks } from './check.svelte';
	import { checkingPolicies } from './running';

	interface Props {
		status?: string;
		/** The image reference, for the tooltip and the toast. */
		image?: string;
		/** The update policy covering the image (checks need one). */
		policyId?: string;
		/** The user may check that policy (update.check). */
		canCheck?: boolean;
	}

	let { status, image, policyId, canCheck = false }: Props = $props();
	// Outside a query client (component tests) the check still runs; live
	// events refresh the lists then.
	let queryClient: QueryClient | undefined;
	try {
		queryClient = useQueryClient();
	} catch {
		queryClient = undefined;
	}

	const clickable = $derived(!!policyId && canCheck);
	// The running jobs list (shared by every badge; only where a check can
	// be started, since only the button shows it).
	const activeJobs = queryClient
		? createQuery(() => ({ ...activeJobsQuery(), enabled: clickable }))
		: null;
	const info = $derived(updateBadge(status));
	const checking = $derived(
		!!policyId &&
			(updateChecks.has(policyId) || checkingPolicies(activeJobs?.data).has(policyId))
	);
	const Icon = $derived(
		status === 'update_available'
			? CircleArrowUp
			: status === 'up_to_date'
				? CircleCheck
				: status === 'quarantined'
					? ShieldAlert
					: status === 'unchecked'
						? CircleDashed
						: CircleAlert
	);
	const what = $derived(image || 'this image');
	const label = $derived(
		checking
			? `Checking ${what} for updates`
			: `${info?.text ?? ''}${clickable ? '. Select to check again.' : '.'}`
	);

	function check(e: MouseEvent) {
		e.preventDefault();
		e.stopPropagation();
		if (policyId && !checking) void checkForUpdates(policyId, what, { queryClient });
	}
</script>

{#if info || checking}
	{#if clickable}
		<button
			type="button"
			class="update {info?.tone ?? 'muted'}"
			class:checking
			aria-label={label}
			title={label}
			aria-busy={checking || undefined}
			aria-disabled={checking || undefined}
			onclick={check}
		>
			{#if checking}<RefreshCw size={14} strokeWidth={2} aria-hidden="true" />{:else}<Icon
					size={14}
					strokeWidth={2}
					aria-hidden="true"
				/>{/if}
		</button>
	{:else}
		<span class="update {info?.tone ?? 'muted'}" role="img" aria-label={label} title={label}>
			<Icon size={14} strokeWidth={2} aria-hidden="true" />
		</span>
	{/if}
{/if}

<style>
	.update {
		display: inline-grid;
		flex-shrink: 0;
		place-items: center;
		width: 20px;
		height: 20px;
		padding: 0;
		border: 0;
		border-radius: var(--radius-full);
		background: transparent;
		vertical-align: middle;
	}

	button.update {
		cursor: pointer;
		transition: background-color var(--duration-fast) var(--ease-out);
	}

	button.update:hover:not([aria-disabled='true']) {
		background: var(--surface-hover);
	}

	button.update:focus-visible {
		outline: var(--focus-ring);
		outline-offset: 1px;
	}

	button.update[aria-disabled='true'] {
		cursor: progress;
	}

	.warn {
		color: var(--warn);
	}

	.ok {
		color: var(--ok);
	}

	.danger {
		color: var(--danger);
	}

	.muted {
		color: var(--text-muted);
	}

	.checking :global(svg) {
		animation: update-spin 0.9s linear infinite;
	}

	@keyframes update-spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
