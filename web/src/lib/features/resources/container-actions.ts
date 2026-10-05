// Container lifecycle requests (#6): each answers 202 with a job. Which
// actions a container offers follows its state and the DTO's granted
// actions (#17); Docker Manager's own containers (#32) still offer them so the
// server's refusal and its reason are shown (the page also says up front
// what is refused). Recreate (#273) is offered only for standalone
// containers that are not Docker Manager's own: a stack's containers are
// recreated through the stack. Remove is not offered where the server
// always refuses it (#282): Docker Manager's own containers and those of a
// managed stack (they go with the stack's Stop or Delete).
import { api, unwrap, type ApiClient, type Job } from '$lib/api/client';
import type { Container } from '$lib/api/queries';
import { idempotencyKey } from './jobs.svelte';
import { can } from './permissions';

export type ContainerVerb =
	'start' | 'stop' | 'restart' | 'pause' | 'unpause' | 'recreate' | 'remove';

export interface ContainerAction {
	verb: ContainerVerb;
	label: string;
	capability: string;
	/** Destructive: confirmed with its consequences first. */
	danger?: boolean;
}

const ACTIONS: Record<ContainerVerb, ContainerAction> = {
	start: { verb: 'start', label: 'Start', capability: 'container.start' },
	stop: { verb: 'stop', label: 'Stop', capability: 'container.stop', danger: true },
	restart: { verb: 'restart', label: 'Restart', capability: 'container.restart' },
	pause: { verb: 'pause', label: 'Pause', capability: 'container.pause' },
	unpause: { verb: 'unpause', label: 'Unpause', capability: 'container.unpause' },
	recreate: {
		verb: 'recreate',
		label: 'Recreate',
		capability: 'container.recreate',
		danger: true
	},
	remove: { verb: 'remove', label: 'Remove', capability: 'container.remove', danger: true }
};

/** The lifecycle actions that make sense in the container's state and are granted. */
export function containerActions(
	c: Pick<Container, 'state' | 'actions' | 'stack' | 'protection'>
): ContainerAction[] {
	const verbs: ContainerVerb[] = [];
	switch (c.state) {
		case 'running':
			verbs.push('stop', 'restart', 'pause');
			break;
		case 'paused':
			verbs.push('unpause', 'stop');
			break;
		case 'restarting':
			verbs.push('stop');
			break;
		case 'removing':
			break;
		default: // created, exited, dead
			verbs.push('start');
	}
	if (c.state !== 'removing') {
		if (!c.stack && !c.protection) verbs.push('recreate');
		if (!c.protection && !c.stack?.managed) verbs.push('remove');
	}
	return verbs.map((v) => ACTIONS[v]).filter((a) => can(c.actions, a.capability));
}

export interface ActionOptions {
	/** remove: kill a running container first. */
	force?: boolean;
	/** remove: also remove anonymous volumes. */
	removeVolumes?: boolean;
	/** restart: confirms interrupting Docker Manager itself (#32 confirmation_required). */
	confirm?: boolean;
}

/** Sends one lifecycle request; resolves to the accepted job. */
export function runContainerAction(
	env: string,
	name: string,
	verb: ContainerVerb,
	opts: ActionOptions = {},
	client: ApiClient = api
): Promise<Job> {
	const path = { environmentId: env, containerId: name };
	const header = { 'Idempotency-Key': idempotencyKey() };
	switch (verb) {
		case 'remove':
			return unwrap(
				client.DELETE('/api/v1/environments/{environmentId}/containers/{containerId}', {
					params: {
						path,
						header,
						query: {
							force: opts.force || undefined,
							removeVolumes: opts.removeVolumes || undefined
						}
					}
				})
			);
		case 'restart':
			return unwrap(
				client.POST(
					'/api/v1/environments/{environmentId}/containers/{containerId}/restart',
					{
						params: { path, header },
						body: opts.confirm ? { confirm: true } : {}
					}
				)
			);
		case 'stop':
			return unwrap(
				client.POST('/api/v1/environments/{environmentId}/containers/{containerId}/stop', {
					params: { path, header },
					body: {}
				})
			);
		case 'recreate':
			return unwrap(
				client.POST(
					'/api/v1/environments/{environmentId}/containers/{containerId}/recreate',
					{
						params: { path, header },
						body: {}
					}
				)
			);
		case 'start':
			return unwrap(
				client.POST('/api/v1/environments/{environmentId}/containers/{containerId}/start', {
					params: { path, header }
				})
			);
		case 'pause':
			return unwrap(
				client.POST('/api/v1/environments/{environmentId}/containers/{containerId}/pause', {
					params: { path, header }
				})
			);
		case 'unpause':
			return unwrap(
				client.POST(
					'/api/v1/environments/{environmentId}/containers/{containerId}/unpause',
					{
						params: { path, header }
					}
				)
			);
	}
}
