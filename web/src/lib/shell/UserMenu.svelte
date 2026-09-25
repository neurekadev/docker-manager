<script lang="ts">
	// User menu (#22): the signed-in account, security, API tokens, sign out.
	import KeyRound from '@lucide/svelte/icons/key-round';
	import LogOut from '@lucide/svelte/icons/log-out';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import type { Account } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import Menu from '$lib/ui/Menu.svelte';
	import type { MenuEntry } from '$lib/ui/menu';

	let { user, onsignout }: { user: Account; onsignout: () => void } = $props();
	const name = $derived(user.displayName || user.username);
	const initial = $derived(name.trim()[0]?.toUpperCase() ?? '?');

	const items = $derived<MenuEntry[]>([
		{ heading: user.owner ? `${name} (owner)` : name },
		{ label: 'Profile and security', icon: ShieldCheck, href: routes.security() },
		{ label: 'API tokens', icon: KeyRound, href: routes.apiTokens() },
		{ separator: true },
		{ label: 'Sign out', icon: LogOut, onSelect: onsignout }
	]);
</script>

<Menu {items} label="Account">
	{#snippet trigger(props)}
		<button {...props} type="button" class="avatar" aria-label="Account menu for {name}">
			<span aria-hidden="true">{initial}</span>
		</button>
	{/snippet}
</Menu>

<style>
	.avatar {
		display: grid;
		place-items: center;
		width: 32px;
		height: 32px;
		padding: 0;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-full);
		background: var(--surface-raised);
		color: var(--text-strong);
		font-size: var(--text-body);
		font-weight: var(--weight-semibold);
	}

	.avatar:hover,
	.avatar[data-state='open'] {
		background: var(--surface-hover);
	}

	@media (pointer: coarse) {
		.avatar {
			width: var(--touch-target);
			height: var(--touch-target);
		}
	}
</style>
