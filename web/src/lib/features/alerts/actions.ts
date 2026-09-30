// Dismissing alerts (#159) from the Alerts page and the bell: one alert,
// or several at once ("Dismiss all"). A dismissal is for everyone: the
// alert leaves the bell, the dashboard and the environment's notice and
// opens again when it gets worse. The toast repeats the action ("Dismissed
// Disk /dev/sda on homelab is failing", "Dismissed 3 alerts"); a failure
// says what happened. Every alerts list is refreshed afterwards (live
// events do it too); nothing is retried.
import type { QueryClient } from '@tanstack/svelte-query';
import { api, type ApiClient } from '$lib/api/client';
import { actionError } from '$lib/features/common/errors';
import { toast as appToast, type Toasts } from '$lib/ui/toast.svelte';
import { alertCount, type Alert } from './model';
import { alertKeys, dismissAlert, dismissAlerts } from './queries';

export interface DismissOptions {
	queryClient?: QueryClient;
	client?: ApiClient;
	toast?: Pick<Toasts, 'success' | 'error'>;
	/** No success toast (the bell: the item leaves the list in place). */
	quiet?: boolean;
}

const ERRORS = {
	alert_not_firing: 'It was resolved in the meantime, so there is nothing to dismiss.',
	forbidden: 'You may not dismiss it. Ask someone who manages its environment, job or policy.',
	not_found: 'It is gone, or you can no longer see it.'
};

function refresh(o: DismissOptions) {
	void o.queryClient?.invalidateQueries({ queryKey: alertKeys.all });
}

/** Dismisses one alert; true when it worked. */
export async function dismissOne(
	a: Pick<Alert, 'id' | 'title'>,
	o: DismissOptions = {}
): Promise<boolean> {
	const t = o.toast ?? appToast;
	try {
		await dismissAlert(a.id, o.client ?? api);
		if (!o.quiet) t.success(`Dismissed ${a.title}`);
		return true;
	} catch (e) {
		t.error(`Couldn’t dismiss ${a.title}`, { body: actionError(e, ERRORS) });
		return false;
	} finally {
		refresh(o);
	}
}

/**
 * Dismisses several alerts (those the caller may dismiss); returns how
 * many are dismissed. Throws on failure (for ConfirmDialog, which shows
 * the error) unless `report` is set, which toasts it and returns null.
 */
export async function dismissMany(
	ids: readonly string[],
	o: DismissOptions & { report?: boolean } = {}
): Promise<number | null> {
	const t = o.toast ?? appToast;
	if (ids.length === 0) return 0;
	try {
		const n = await dismissAlerts(ids, o.client ?? api);
		if (!o.quiet) t.success(`Dismissed ${alertCount(n)}`);
		return n;
	} catch (e) {
		if (!o.report) throw e;
		t.error('Couldn’t dismiss the alerts', { body: actionError(e, ERRORS) });
		return null;
	} finally {
		refresh(o);
	}
}
