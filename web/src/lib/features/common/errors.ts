// Error copy of edits and actions (#22 copy rules: what happened and what
// to do next). Switches on stable API codes, never on messages.
import { errorView } from '$lib/ui/errors';

/** Codes whose meaning is the same everywhere in the admin screens. */
const COMMON: Record<string, string> = {
	precondition_failed:
		'Someone else changed this since you opened it. Reload to see the current version, then make your change again.',
	precondition_required: 'The change needs the current version. Reload and try again.',
	environment_offline:
		"The environment is offline. It reconnects automatically; try again when it's back.",
	api_token_not_allowed: 'This needs a signed-in browser session, not an API token.'
};

/** The user-facing message of a failed change. */
export function actionError(e: unknown, overrides: Record<string, string> = {}): string {
	const v = errorView(e);
	if (v.code && overrides[v.code]) return overrides[v.code];
	if (v.code && COMMON[v.code]) return COMMON[v.code];
	return v.message;
}

/** Field errors of a failed change, keyed by field path ("body.name"). */
export function fieldErrors(e: unknown): Record<string, string> {
	const out: Record<string, string> = {};
	for (const f of errorView(e).fields) out[f.field] = f.message;
	return out;
}
