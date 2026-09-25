// Agent enrollment (#3, #34): intents, token lifetimes and the words the
// add-environment flow uses. Pure.
import type { AgentEnrollment } from '$lib/api/client';

/** What an enrollment is for, in the user's words. */
export function enrollmentIntent(intent: string): string {
	if (!intent || intent === 'new') return 'New environment';
	if (intent.startsWith('replace:')) return 'Replaces an agent on the same Engine';
	if (intent.startsWith('reattach:')) return 'Re-attaches an archived environment';
	return intent;
}

/** StatusBadge key of an enrollment state. */
export function enrollmentStateStatus(state: AgentEnrollment['state']): string {
	switch (state) {
		case 'pending':
			return 'queued';
		case 'used':
			return 'succeeded';
		default:
			return state;
	}
}

/** Token lifetimes offered (the API allows 1 min to 24 h; default 1 h). */
export const TOKEN_LIFETIMES = [
	{ value: '900', label: '15 minutes' },
	{ value: '3600', label: '1 hour' },
	{ value: '14400', label: '4 hours' },
	{ value: '86400', label: '24 hours' }
];

/** Order and titles of the generated install commands. */
export const INSTALL_VARIANTS: Record<string, { order: number; heading: string }> = {
	colocated: { order: 0, heading: 'On the DockYard host' },
	remote: { order: 1, heading: 'On another Docker host' },
	remote_compose: { order: 2, heading: 'On another Docker host, with Compose' }
};

/** Enrollment rejection codes in the user's words (the agent got a 409). */
export const REJECTIONS: Record<string, string> = {
	engine_already_enrolled:
		'This Docker Engine already has an agent. Remove that agent first, or create a token that replaces it.',
	engine_identity_conflict:
		'Another environment reports the same Engine ID (a cloned machine?). Create a token that allows a duplicate Engine ID if this is a different host.',
	environment_archived: 'The environment is archived. Re-attach it from the archived list.',
	environment_detached: 'The environment has no agent. Re-attach it with a new token.',
	engine_mismatch:
		'The agent runs on a different Engine than the environment it should re-attach.',
	enrollment_target_unavailable: 'The environment or agent this token targets no longer exists.'
};
