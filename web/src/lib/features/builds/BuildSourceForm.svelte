<script lang="ts">
	// The Git build source (#33): repository URL, ref, the Git credential
	// for private repositories and the image names first; context,
	// Dockerfile, target stage, platform, build arguments (with the
	// image-history warning) and options under "Advanced" (open when one
	// is set). Used by the manual build page and the build definition
	// dialog.
	import { untrack } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { gitCredentialsQuery } from '$lib/api/queries';
	import { Checkbox, Notice, Select, TextArea, TextField } from '$lib/ui';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import type { SourceErrors, SourceForm } from './source';

	interface Props {
		form: SourceForm;
		errors: SourceErrors;
		/** Server field errors by field name (e.g. "gitUrl"). */
		serverError?: (field: string) => string | undefined;
		/** Show the Git credential choice (needs git_credential.read to list them). */
		credentials?: boolean;
	}

	let {
		form = $bindable(),
		errors,
		serverError = () => undefined,
		credentials = true
	}: Props = $props();

	const creds = createQuery(() => ({
		...gitCredentialsQuery(),
		enabled: credentials,
		retry: false
	}));
	const host = $derived(form.gitUrl.replace(/^https?:\/\//, '').split('/')[0] ?? '');
	// Opens when the form gets an advanced value from outside (a definition,
	// a build again); closing it stays the user's choice.
	const hasAdvanced = $derived(
		!!(
			form.contextPath ||
			form.dockerfile ||
			form.target ||
			form.platform ||
			form.buildArgs ||
			form.noCache ||
			form.pull
		)
	);
	let advancedOpen = $state(false);
	$effect(() => {
		if (hasAdvanced) untrack(() => (advancedOpen = true));
	});
	const matching = $derived(
		(creds.data ?? []).filter((c) => c.status === 'active' && (!host || c.host === host))
	);
</script>

<div class="grid">
	<div class="wide">
		<TextField
			label="Repository URL"
			mono
			required
			bind:value={form.gitUrl}
			placeholder="https://github.com/acme/app.git"
			description="HTTPS only."
			error={errors.gitUrl ?? serverError('gitUrl')}
			autocomplete="off"
			spellcheck="false"
		/>
	</div>
	<TextField
		label="Ref"
		mono
		bind:value={form.ref}
		placeholder="main"
		optional
		description="Branch, tag or commit. Default: the default branch."
		error={serverError('ref')}
	/>
	{#if credentials && creds.data}
		<Select
			label="Git Credential"
			bind:value={form.gitCredentialId}
			description={matching.length
				? 'For private repositories.'
				: `No credential matches ${host || 'this host'}: public repositories only.`}
			options={[
				{ value: '', label: 'Matching Credential (or None)' },
				...matching.map((c) => ({
					value: c.id,
					label: `${c.name} (${c.host}${c.pathPrefix ? `/${c.pathPrefix}` : ''})`
				}))
			]}
		/>
	{/if}
	<div class="wide">
		<TextArea
			label="Image Names"
			mono
			required
			rows={2}
			bind:value={form.tags}
			placeholder="registry.example.com/acme/app:1.4"
			description="One name:tag per line."
			error={errors.tags ?? serverError('tags')}
		/>
	</div>
	<div class="wide">
		<Disclosure summary="Advanced" open={advancedOpen}>
			<div class="grid">
				<TextField
					label="Context Directory"
					mono
					bind:value={form.contextPath}
					placeholder="services/api"
					optional
					description="Default: the repository root."
					error={serverError('contextPath')}
				/>
				<TextField
					label="Dockerfile"
					mono
					bind:value={form.dockerfile}
					placeholder="Dockerfile"
					optional
					description="Relative to the context."
					error={errors.dockerfile ?? serverError('dockerfile')}
				/>
				<TextField
					label="Target Stage"
					mono
					bind:value={form.target}
					optional
					description="Default: the last stage."
					error={serverError('target')}
				/>
				<TextField
					label="Platform"
					mono
					bind:value={form.platform}
					placeholder="linux/amd64"
					optional
					description="Default: the environment's platform."
					error={serverError('platform')}
				/>
				<div class="wide">
					<TextArea
						label="Build Arguments"
						mono
						rows={3}
						bind:value={form.buildArgs}
						placeholder="NODE_VERSION=22"
						optional
						description="One KEY=value per line."
						error={errors.buildArgs ?? serverError('buildArgs')}
					/>
					<div class="warn">
						<Notice
							tone="warn"
							icon={TriangleAlert}
							title="Build arguments are visible in the image history"
							live="none"
						>
							Anyone who can pull the image can read them. Never pass passwords,
							tokens or keys as build arguments.
						</Notice>
					</div>
				</div>
				<div class="checks wide">
					<Checkbox label="Build Without Cache" bind:checked={form.noCache} />
					<Checkbox label="Pull Newer Base Images" bind:checked={form.pull} />
				</div>
			</div>
		</Disclosure>
	</div>
</div>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-4);
	}

	.wide {
		grid-column: 1 / -1;
	}

	.warn {
		margin-top: var(--space-3);
	}

	.checks {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-3);
	}

	@media (max-width: 767px) {
		.grid,
		.checks {
			grid-template-columns: 1fr;
		}
	}
</style>
