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

/**
 * Order, titles and plain-words descriptions of the generated install
 * commands (the server's own descriptions name configuration variables
 * and files; they are the fallback for variants this client does not know).
 */
export const INSTALL_VARIANTS: Record<
	string,
	{ order: number; heading: string; description: string }
> = {
	colocated: {
		order: 0,
		heading: 'On the Docker Manager host',
		description:
			'Run it in the folder with Docker Manager’s compose.yaml. The agent there is already running; it connects within seconds.'
	},
	remote: {
		order: 1,
		heading: 'On another Docker host',
		description:
			'Starts the agent on that host and hands it the one-time token. The agent controls Docker on the host, so run it only on hosts you manage.'
	},
	remote_compose: {
		order: 2,
		heading: 'On another Docker host, with Compose',
		description:
			'Put these lines in the .env file next to the agent’s compose.yaml, then start it. Remove the token from the file once the host shows as connected.'
	}
};

/** Enrollment rejection codes in the user's words (the agent got a 409). */
export const REJECTIONS: Record<string, string> = {
	engine_already_enrolled:
		'Docker on this host is already connected through another agent. Remove that agent first.',
	engine_identity_conflict:
		'Docker Manager already knows a host that looks exactly like this one (a cloned machine?). If it is a different host, create a new command with “This host is a clone” under More options.',
	environment_archived: 'The environment is archived. Re-attach it from the archived list.',
	environment_detached: 'The environment has no agent. Re-attach it with a new command.',
	engine_mismatch: 'The agent runs on a different host than the environment it should re-attach.',
	enrollment_target_unavailable:
		'The environment or agent this command was made for no longer exists.'
};
