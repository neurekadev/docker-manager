// Starting stack deploys from the stack page (#7). The header's Deploy
// menu and the overview's drift notice share one "deploy and remove
// orphaned containers" request (the confirmation lives in the header).
// Deploys are tracked in the page's job tray; their success toast says
// what actually changed (e.g. a deploy that started no container).
import type { QueryClient } from '@tanstack/svelte-query';
import { deployStackWith } from './actions';
import { deployFailure, deploySuccess, deployTitle, stackTitle, type DeployChoice } from './model';
import { stackQuery, type Stack } from './queries';
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
