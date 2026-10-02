<script lang="ts">
	// User menu (#22): the signed-in account, the personal Profile (account
	// and sign-in factors) and its API tokens tab, sign out. Instance
	// administration is Settings in the sidebar, not here.
	import KeyRound from '@lucide/svelte/icons/key-round';
	import LogOut from '@lucide/svelte/icons/log-out';
	import UserRound from '@lucide/svelte/icons/user-round';
	import type { Account } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import Menu from '$lib/ui/Menu.svelte';
	import type { MenuEntry } from '$lib/ui/menu';

	let { user, onsignout }: { user: Account; onsignout: () => void } = $props();
	const name = $derived(user.displayName || user.username);
	const initial = $derived(name.trim()[0]?.toUpperCase() ?? '?');

	const items = $derived<MenuEntry[]>([
		{ heading: user.owner ? `${name} (owner)` : name },
		{ label: 'Profile', icon: UserRound, href: routes.profile() },
		{ label: 'API Tokens', icon: KeyRound, href: routes.apiTokens() },
		{ separator: true },
		{ label: 'Sign Out', icon: LogOut, onSelect: onsignout }
	]);
</script>

<Menu {items} label="Account">
	{#snippet trigger(props)}
		<button {...props} type="button" class="avatar" aria-label="Account Menu for {name}">
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
