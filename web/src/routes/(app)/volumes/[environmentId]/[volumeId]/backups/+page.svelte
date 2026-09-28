<script lang="ts">
	// Backups (#10 volume tab): the volume's own backups and the stack
	// backups that hold it; restoring one replaces only this volume (whole,
	// or the files chosen in the picker).
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { environmentsQuery } from '$lib/api/queries';
	import BackupsTab from '$lib/features/backups/BackupsTab.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';

	const env = $derived(page.params.environmentId ?? '');
	const name = $derived(page.params.volumeId ?? '');
	const envs = createQuery(() => environmentsQuery());
	const envName = $derived(envs.data?.find((e) => e.id === env)?.name ?? env);

	// The same trail as the volume's other tabs: Volumes / env / name / Backups.
	usePage(() => ({
		title: `${name} backups`,
		crumbs: [
			{ label: 'Volumes', href: routes.volumes() },
			{ label: envName },
			{ label: name, href: routes.volume(env, name) },
			{ label: 'Backups' }
		]
	}));
</script>

<BackupsTab filter={{ environmentId: env, volume: name }} subject={name} volume={name} />
