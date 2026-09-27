// Starting stack deploys and pulls from the stack page (#7). The header's
// Deploy menu and the overview's drift notice share one "deploy and remove
// orphaned containers" request (the confirmation lives in the header).
// Deploys and pulls are tracked in the page's job tray; their success
// toast says what actually changed (a deploy that started no container,
// a pull that found nothing newer).
import type { QueryClient } from '@tanstack/svelte-query';
import { deployStackWith, pullStack } from './actions';
import {
	deployFailure,
	deploySuccess,
	deployTitle,
	pullResult,
	stackTitle,
	type DeployChoice
} from './model';
import {
	stackImageStatusQuery,
	stackKeys,
	stackQuery,
	type Stack,
	type StackImageStatus
} from './queries';
import type { JobTray } from './tray.svelte';

/** Opens the "deploy and remove orphaned containers" confirmation. */
export class RemoveOrphansRequest {
	open = $state(false);

	request() {
		this.open = true;
	}
}

/**
 * Starts a deploy and adds it to the tray. When it succeeds, the stack is
 * read again: an unchanged last deploy time means the deploy started no
 * container, and the toast says so.
 */
export async function startDeploy(
	stack: Stack,
	choice: DeployChoice,
	tray: JobTray,
	queryClient: QueryClient
): Promise<void> {
	const title = stackTitle(stack);
	const before = stack.appliedRevision?.at;
	const job = await deployStackWith(stack.id, choice);
	tray.add(job, {
		title: deployTitle(title, choice),
		success: deploySuccess(title, choice, undefined, 'changed'),
		failure: deployFailure(title, choice),
		successFor: async () => {
			const cur = await queryClient.fetchQuery({ ...stackQuery(stack.id), staleTime: 0 });
			return deploySuccess(stackTitle(cur), choice, before, cur.appliedRevision?.at);
		}
	});
}

/**
 * Starts a pull (images only, no container changes) and adds it to the
 * tray. When it succeeds, the image status is read again: services with a
 * newer image on the host are named and a Deploy action offered.
 */
export async function startPull(
	stack: Stack,
	tray: JobTray,
	queryClient: QueryClient,
	deploy?: () => void
): Promise<void> {
	const title = stackTitle(stack);
	const before = queryClient.getQueryData<StackImageStatus[]>(stackKeys.imageStatus(stack.id));
	const job = await pullStack(stack.id);
	tray.add(job, {
		title: `Pull ${title}`,
		success: `Pulled the images of ${title}`,
		failure: `The images of ${title} were not pulled`,
		successFor: async () => {
			const after = stack.actions.includes('stack.read')
				? await queryClient.fetchQuery({ ...stackImageStatusQuery(stack.id), staleTime: 0 })
				: undefined;
			const r = pullResult(title, before, after);
			return {
				title: r.title,
				body: r.body,
				action: r.newer.length && deploy ? { label: 'Deploy', onclick: deploy } : undefined
			};
		}
	});
}
