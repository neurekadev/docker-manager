// Sending a test message (#142) from the channel list or the toast after
// adding a channel: the toast says it was sent, or what failed and what to
// check. The manager allows one test per channel every 5 seconds.
import type { QueryClient } from '@tanstack/svelte-query';
import { ApiRequestError, api, type ApiClient } from '$lib/api/client';
import { errorMessage, toast } from '$lib/ui';
import { testOutcome } from './model';
import { notificationKeys, sendTest } from './queries';

export async function runTest(
	c: { id: string; name: string },
	queryClient?: QueryClient,
	client: ApiClient = api
): Promise<void> {
	try {
		const o = testOutcome(c.name, await sendTest(c.id, client));
		if (o.ok) toast.success(o.title);
		else toast.error(o.title, { body: o.body });
	} catch (e) {
		if (e instanceof ApiRequestError && e.apiError?.code === 'notification_test_rate_limited')
			toast.info(`A test message was just sent to ${c.name}`, {
				body: 'Wait a few seconds before sending another one.'
			});
		else
			toast.error(`The test message to ${c.name} could not be sent`, {
				body: errorMessage(e)
			});
	} finally {
		void queryClient?.invalidateQueries({ queryKey: notificationKeys.all });
	}
}
