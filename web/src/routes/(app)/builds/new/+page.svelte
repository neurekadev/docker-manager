<script lang="ts">
	// Manual Git build (#33): an environment and the Git source; the build
	// runs as a job on the environment's agent (BuildKit, no Docker CLI) and
	// the page moves to the build's live log. Optionally saved as a build
	// definition to run again later.
	import { untrack } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Hammer from '@lucide/svelte/icons/hammer';
	import Play from '@lucide/svelte/icons/play';
	import { api, unwrap } from '$lib/api/client';
	import { queryKeys } from '$lib/api/queries';
	import { criticalWork } from '$lib/live';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		Checkbox,
		DeniedState,
		Notice,
		PageHeader,
		Select,
		TextField,
		errorMessage,
		fieldError,
		toast
	} from '$lib/ui';
	import BuildSourceForm from '$lib/features/builds/BuildSourceForm.svelte';
	import {
		emptyForm,
		isComplete,
		toSource,
		validateSource,
		type SourceForm
	} from '$lib/features/builds/source';
	import Page from '$lib/features/resources/Page.svelte';
	import { idempotencyKey } from '$lib/features/resources/jobs.svelte';
	import { refusal } from '$lib/features/resources/refusals';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({
		title: 'Build an image',
		crumbs: [{ label: 'Builds', href: routes.builds() }, { label: 'Build an image' }],
		environmentScoped: true
	});

	const queryClient = useQueryClient();
	const scope = useEnvironmentScope();
	const allowed = $derived(
		scope.targets.filter((t) => t.online && scope.can('image.build', t.id))
	);
	let env = $state('');
	const canSave = $derived(scope.can('build_definition.manage', env));
	$effect(() => {
		if (env || !allowed.length) return;
		const want = untrack(() => page.url.searchParams.get('environment'));
		env = allowed.find((e) => e.id === want)?.id ?? allowed[0].id;
	});
	let form = $state<SourceForm>(emptyForm());
	let save = $state(false);
	let saveName = $state('');
	let busy = $state(false);
	let failure = $state<unknown>(null);

	let release: (() => void) | null = null;
	$effect(() => {
		const dirty = !!(form.gitUrl || form.tags || form.buildArgs);
		if (dirty && !release) release = criticalWork.register('unsaved-edit', 'the build form');
		if (!dirty && release) {
			release();
			release = null;
		}
	});
	$effect(() => () => release?.());

	const errors = $derived(validateSource(form));
	const valid = $derived(
		!!env &&
			isComplete(form) &&
			Object.keys(errors).length === 0 &&
			(!save || !!saveName.trim())
	);
	const serverError = (f: string) =>
		fieldError(failure, `body.${f}`) ?? fieldError(failure, `body.source.${f}`);

	async function build() {
		busy = true;
		failure = null;
		const source = toSource(form);
		try {
			if (save) {
				await unwrap(
					api.POST('/api/v1/environments/{environmentId}/build-definitions', {
						params: { path: { environmentId: env } },
						body: { name: saveName.trim(), source }
					})
				);
			}
			const job = await unwrap(
				api.POST('/api/v1/environments/{environmentId}/images/builds', {
					params: {
						path: { environmentId: env },
						header: { 'Idempotency-Key': idempotencyKey() }
					},
					body: source
				})
			);
			release?.();
			release = null;
			toast.info(`Building ${source.tags[0]}`);
			void queryClient.invalidateQueries({ queryKey: queryKeys.images.all });
			await goto(routes.build(env, job.id));
		} catch (e) {
			failure = e;
		} finally {
			busy = false;
		}
	}

	const failureText = $derived.by(() => {
		if (!failure) return null;
		const r = refusal(failure, {
			kind: 'image',
			name: form.tags.split('\n')[0] || 'The image',
			verb: 'build',
			environmentName: scope.name(env)
		});
		return r.body ? `${r.title} ${r.body}` : r.title;
	});
</script>

{#if scope.ready && scope.envs.data && !allowed.length}
	<DeniedState
		level={1}
		title="You can't build images here."
		description="Building needs the permission on an online environment. Ask the owner of this DockYard if you need it."
	/>
{:else}
	<Page>
		<PageHeader
			title="Build an image"
			description="From a Git repository, on the environment's own Docker Engine."
			icon={Hammer}
			color="violet"
		/>
		<form
			class="form"
			onsubmit={(e) => {
				e.preventDefault();
				if (valid) void build();
			}}
		>
			<Card title="Where">
				<div class="where">
					<Select
						label="Environment"
						bind:value={env}
						options={allowed.map((e) => ({ value: e.id, label: e.name }))}
						description="The image is built and stored there."
					/>
				</div>
			</Card>
			<Card title="Source">
				<BuildSourceForm
					bind:form
					{errors}
					{serverError}
					credentials={scope.hasAny('git_credential.read')}
				/>
			</Card>
			{#if canSave}
				<Card title="Save for later">
					<div class="save">
						<Checkbox
							label="Save as a build definition"
							description="Run the same build again from Definitions."
							bind:checked={save}
						/>
						{#if save}
							<TextField
								label="Definition name"
								required
								bind:value={saveName}
								placeholder="silo-web"
								error={fieldError(failure, 'body.name')}
							/>
						{/if}
					</div>
				</Card>
			{/if}
			{#if failureText && !Object.keys(errors).length}
				<Notice tone="danger" title="The build didn't start" live="alert"
					>{failureText}</Notice
				>
			{/if}
			<div class="submit">
				<Button variant="ghost" href={routes.builds()}>Cancel</Button>
				<Button type="submit" variant="primary" icon={Play} loading={busy} disabled={!valid}
					>Build image</Button
				>
			</div>
		</form>
		{#if failure && !failureText}<p class="muted">{errorMessage(failure)}</p>{/if}
	</Page>
{/if}

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.where {
		max-width: 420px;
	}

	.save {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
		gap: var(--space-4);
		align-items: start;
	}

	.submit {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-3);
	}
</style>
