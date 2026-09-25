<script lang="ts">
	// The Git build source (#33): repository URL, ref, context, Dockerfile,
	// target, build arguments (with the image-history warning), options,
	// tags and the Git credential for private repositories. Used by the
	// manual build page and the build definition dialog.
	import { createQuery } from '@tanstack/svelte-query';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { gitCredentialsQuery } from '$lib/api/queries';
	import { Checkbox, Notice, Select, TextArea, TextField } from '$lib/ui';
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
			description="HTTPS only. DockYard resolves the ref to a commit and builds exactly that commit."
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
		description="Branch, tag or commit. Optional; default: the repository's default branch."
		error={serverError('ref')}
	/>
	<TextField
		label="Context directory"
		mono
		bind:value={form.contextPath}
		placeholder="services/api"
		description="Optional. Default: the repository root."
		error={serverError('contextPath')}
	/>
	<TextField
		label="Dockerfile"
		mono
		bind:value={form.dockerfile}
		placeholder="Dockerfile"
		description="Relative to the context. Optional."
		error={errors.dockerfile ?? serverError('dockerfile')}
	/>
	<TextField
		label="Target stage"
		mono
		bind:value={form.target}
		description="Optional. Default: the last stage."
		error={serverError('target')}
	/>
	<div class="wide">
		<TextArea
			label="Image names"
			mono
			required
			rows={2}
			bind:value={form.tags}
			placeholder="registry.example.com/acme/app:1.4"
			description="One name:tag per line. The built image gets every name."
			error={errors.tags ?? serverError('tags')}
		/>
	</div>
	<div class="wide">
		<TextArea
			label="Build arguments"
			mono
			rows={3}
			bind:value={form.buildArgs}
			placeholder="NODE_VERSION=22"
			description="Optional. One KEY=value per line."
			error={errors.buildArgs ?? serverError('buildArgs')}
		/>
		<div class="warn">
			<Notice
				tone="warn"
				icon={TriangleAlert}
				title="Build arguments are visible in the image history"
				live="none"
			>
				Anyone who can pull the image can read them. Never pass passwords, tokens or keys as
				build arguments.
			</Notice>
		</div>
	</div>
	{#if credentials && creds.data}
		<Select
			label="Git credential"
			bind:value={form.gitCredentialId}
			description={matching.length
				? 'For private repositories. Default: the credential that matches the repository.'
				: `No credential matches ${host || 'this host'}: public repositories only.`}
			options={[
				{ value: '', label: 'Matching credential (or none)' },
				...matching.map((c) => ({
					value: c.id,
					label: `${c.name} (${c.host}${c.pathPrefix ? `/${c.pathPrefix}` : ''})`
				}))
			]}
		/>
	{/if}
	<TextField
		label="Platform"
		mono
		bind:value={form.platform}
		placeholder="linux/amd64"
		description="Optional. Default: the environment's platform."
		error={serverError('platform')}
	/>
	<div class="checks wide">
		<Checkbox
			label="Build without cache"
			description="Runs every step again (slower)."
			bind:checked={form.noCache}
		/>
		<Checkbox
			label="Pull newer base images"
			description="Checks the registry for newer FROM images first."
			bind:checked={form.pull}
		/>
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
