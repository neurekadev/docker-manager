<script lang="ts">
	// Runs container lifecycle actions for a list or a detail page (#6):
	// start, restart, pause and unpause go straight to the server; stop and
	// remove confirm first with their consequences (the server's removal
	// preview); restarting Docker Manager's own manager needs the explicit
	// confirmation of #32. Refusals (protected, stack_managed, offline, ...)
	// are shown with the server's reason, in the dialog or as a toast.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { ApiRequestError, type Job, type Schema } from '$lib/api/client';
	import { containerQuery, queryKeys, type Container } from '$lib/api/queries';
	import { ConfirmDialog, toast } from '$lib/ui';
	import RemovalDialog from './RemovalDialog.svelte';
	import { runContainerAction, type ContainerVerb } from './container-actions';
	import { trackJob } from './jobs.svelte';
	import { refusal, RefusalError, type RefusalContext } from './refusals';

	interface Props {
		/** Environment name for messages ("homelab is offline"). */
		environmentName?: (env: string) => string | undefined;
		/** Called after a removal was accepted (e.g. leave the detail page). */
		onremoved?: (c: Container) => void;
		/** Called with each job an action started (the page shows its progress). */
		onstarted?: (job: Job) => void;
	}

	let { environmentName, onremoved, onstarted }: Props = $props();
	const queryClient = useQueryClient();

	let target = $state<Container | null>(null);
	let stopOpen = $state(false);
	let removeOpen = $state(false);
	let restartOpen = $state(false);
	let removal = $state<Schema<'Removal'> | undefined>(undefined);

	function ctx(c: Container, verb: ContainerVerb): RefusalContext {
		return {
			kind: 'container',
			name: c.name,
			verb,
			protection: c.protection,
			environmentName: environmentName?.(c.environmentId)
		};
	}

	/** Sends the request and follows its job; throws a readable Error on refusal. */
	async function send(
		c: Container,
		verb: ContainerVerb,
		opts: Parameters<typeof runContainerAction>[3] = {}
	) {
		try {
			const job = await runContainerAction(c.environmentId, c.name, verb, opts);
			onstarted?.(job);
			trackJob(job, {
				ctx: ctx(c, verb),
				queryClient,
				invalidate: [queryKeys.containers.all, ['stacks', 'services']],
				onfinish: (j) => {
					if (verb === 'remove' && j.state === 'succeeded') onremoved?.(c);
				}
			});
		} catch (e) {
			if (
				verb === 'restart' &&
				e instanceof ApiRequestError &&
				e.apiError?.code === 'confirmation_required'
			) {
				target = c;
				restartOpen = true;
				return;
			}
			throw new RefusalError(refusal(e, ctx(c, verb)));
		}
	}

	/** Direct actions report refusals as a toast. */
	async function direct(c: Container, verb: ContainerVerb) {
		try {
			await send(c, verb);
		} catch (e) {
			const r = e instanceof RefusalError ? e.refusal : refusal(e, ctx(c, verb));
			toast.error(r.title, { body: r.body });
		}
	}

	/** Asks for (or runs) an action on a container. */
	export async function request(c: Container, verb: ContainerVerb) {
		target = c;
		switch (verb) {
			case 'stop':
				stopOpen = true;
				return;
			case 'restart':
				if (c.protection?.restartAllowed) {
					restartOpen = true;
					return;
				}
				return direct(c, verb);
			case 'remove': {
				let full: Container = c;
				try {
					full = await queryClient.fetchQuery(containerQuery(c.environmentId, c.name));
				} catch {
					// Minimal view or a transient failure: the server still decides.
				}
				target = full;
				removal = full.details?.removal;
				removeOpen = true;
				return;
			}
			default:
				return direct(c, verb);
		}
	}

	const running = $derived(target?.state === 'running' || target?.state === 'restarting');
	const stackNote = $derived(
		target?.stack
			? target.stack.managed
				? `It belongs to the stack ${target.stack.project}; the stack shows it as stopped until you start it or deploy.`
				: `It belongs to the Compose project ${target.stack.project}.`
			: undefined
	);
</script>

{#if target}
	<ConfirmDialog
		bind:open={stopOpen}
		title="Stop {target.name}?"
		consequences={[
			'The container gets a stop signal and is killed if it does not exit in time.',
			'Its restart policy does not start it again; start it when you need it.',
			...(stackNote ? [stackNote] : [])
		]}
		confirmLabel="Stop container"
		tone="danger"
		onconfirm={() => send(target!, 'stop')}
	/>

	<ConfirmDialog
		bind:open={restartOpen}
		title="Restart {target.name}?"
		message="This restarts part of Docker Manager itself. The Docker Manager UI and API disconnect until it is back; this page reconnects on its own."
		consequences={target.protection ? [target.protection.reason] : []}
		confirmLabel="Restart container"
		onconfirm={() => send(target!, 'restart', { confirm: true })}
	/>

	<RemovalDialog
		bind:open={removeOpen}
		kind="container"
		name={target.name}
		{removal}
		affected={[{ label: target.name, detail: target.state }]}
		confirmLabel={running ? 'Stop and remove' : 'Remove container'}
		onconfirm={() => send(target!, 'remove', { force: running })}
	/>
{/if}
