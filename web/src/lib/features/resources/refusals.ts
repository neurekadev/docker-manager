// What a refused or failed Docker operation tells the user (#6, #32, #22
// copy rules): what happened, why (the API's own reason), and what to do.
// Switches on stable error codes and job error classes, never on messages.
import type { Job, Schema } from '$lib/api/client';
import { errorView } from '$lib/ui/errors';

export type Protection = Schema<'ResourceProtection'>;
export type ResourceKind = 'container' | 'image' | 'volume' | 'network';

export type Verb =
	| 'start'
	| 'stop'
	| 'restart'
	| 'pause'
	| 'unpause'
	| 'remove'
	| 'update'
	| 'create'
	| 'pull'
	| 'tag'
	| 'migrate'
	| 'build';

const PAST: Record<Verb, string> = {
	start: 'started',
	stop: 'stopped',
	restart: 'restarted',
	pause: 'paused',
	unpause: 'unpaused',
	remove: 'removed',
	update: 'changed',
	create: 'created',
	pull: 'pulled',
	tag: 'tagged',
	migrate: 'migrated',
	build: 'built'
};

/** "Stopped silo-web" (the toast repeats the button, #22 copy rules). */
export function doneTitle(verb: Verb, name: string): string {
	const p = PAST[verb];
	return `${p[0].toUpperCase()}${p.slice(1)} ${name}`;
}

/** The present participle for progress lines: "Stopping silo-web". */
export function progressTitle(verb: Verb, name: string): string {
	const ing: Record<Verb, string> = {
		start: 'Starting',
		stop: 'Stopping',
		restart: 'Restarting',
		pause: 'Pausing',
		unpause: 'Unpausing',
		remove: 'Removing',
		update: 'Changing',
		create: 'Creating',
		pull: 'Pulling',
		tag: 'Tagging',
		migrate: 'Migrating',
		build: 'Building'
	};
	return `${ing[verb]} ${name}`;
}

/**
 * Server explanations sometimes cite roadmap issues ("(#28)", "from a
 * backup, #10)"); users don't need those references.
 */
export function plain(text: string): string {
	return text
		.replace(/,\s*#\d+\)/g, ')')
		.replace(/\s*\(#\d+(?:,\s*#\d+)*\)/g, '')
		.trim();
}

/** A server reason as a sentence: capitalized, references dropped, final period. */
export function sentence(text: string): string {
	const t = plain(text);
	if (!t) return t;
	const cap = t[0].toUpperCase() + t.slice(1);
	return /[.!?]$/.test(cap) ? cap : `${cap}.`;
}

/** Short label of Docker Manager's own resources (#32). */
export function protectionLabel(p: Protection): string {
	switch (p.role) {
		case 'agent':
			return p.self ? "This environment's Docker Agent" : 'A Docker Agent';
		case 'manager':
			return p.self ? 'The Docker Manager' : 'A Docker Manager';
		case 'docker_manager_project':
			return "Part of Docker Manager's own deployment";
		case 'docker_manager_image':
			return 'An image Docker Manager runs from';
		case 'manager_data':
			return "Docker Manager's data volume";
		case 'agent_state':
			return "The Docker Agent's state volume";
		case 'stacks':
			return 'The Docker Manager stacks volume';
		case 'docker_manager_volume':
			return 'A volume Docker Manager uses';
		case 'docker_manager_network':
			return 'A network Docker Manager uses';
	}
	return 'A Docker Manager system resource';
}

/** The subject of a refused action on a protected resource. */
function protectedSubject(p: Protection | undefined, name: string): string {
	if (!p) return name;
	switch (p.role) {
		case 'agent':
			return p.self ? "Docker Manager's own agent" : `The Docker Agent ${name}`;
		case 'manager':
			return p.self ? 'Docker Manager itself' : `The Docker Manager ${name}`;
		case 'stacks':
			return 'The stacks volume';
		case 'manager_data':
			return "Docker Manager's data volume";
		case 'agent_state':
			return "The agent's state volume";
	}
	return name;
}

export interface Refusal {
	title: string;
	body?: string;
	code?: string;
}

/**
 * A refusal thrown from a dialog's confirm handler: ConfirmDialog shows its
 * message (title and body) inline; toasts use the parts.
 */
export class RefusalError extends Error {
	readonly refusal: Refusal;
	constructor(r: Refusal) {
		super(r.body ? `${r.title} ${r.body}` : r.title);
		this.name = 'RefusalError';
		this.refusal = r;
	}
}

const HOST_ESCAPE =
	'Docker Manager never changes its own containers, images and volumes; use Docker on the host if you really need to.';

export interface RefusalContext {
	kind: ResourceKind;
	name: string;
	verb: Verb;
	protection?: Protection;
	environmentName?: string;
}

function forCode(code: string | undefined, raw: string, ctx: RefusalContext): Refusal | null {
	const message = sentence(raw);
	const { name, verb } = ctx;
	switch (code) {
		case 'protected':
			return {
				code,
				title: `${protectedSubject(ctx.protection, name)} can't be ${PAST[verb]} from Docker Manager.`,
				body: `${message} ${HOST_ESCAPE}`
			};
		case 'stack_managed':
			return {
				code,
				title: `${name} belongs to a stack Docker Manager manages.`,
				body: "Change it through its stack instead: edit the stack's files and deploy."
			};
		case 'container_running':
			return {
				code,
				title: `${name} is running.`,
				body: 'Stop it first, or remove it with "Stop and remove", which kills it.'
			};
		case 'image_in_use':
			return {
				code,
				title: 'Containers still use this image.',
				body: 'Remove the containers that use it first; they are listed on the image.'
			};
		case 'volume_in_use':
			return {
				code,
				title: `Containers still mount ${name}.`,
				body: 'Remove the containers that use it first; they are listed on the volume.'
			};
		case 'network_in_use':
			return {
				code,
				title: `Containers are still attached to ${name}.`,
				body: 'Disconnect or remove them first; they are listed on the network.'
			};
		case 'network_builtin':
			return {
				code,
				title: `${name} is a predefined Docker network.`,
				body: 'Docker needs bridge, host and none; they cannot be removed.'
			};
		case 'recreate_required':
			return {
				code,
				title: 'These settings need a new container.',
				body: `${message} Create a container with the new settings, or manage it in a Compose stack.`
			};
		case 'resource_name_taken':
			return { code, title: `${name} already exists in this environment.`, body: message };
		case 'environment_offline':
			return {
				code,
				title: `${ctx.environmentName ?? 'The environment'} is offline.`,
				body: `${name} can't be ${PAST[verb]} while its agent is disconnected. It reconnects automatically; try again when it's back.`
			};
		case 'environment_archived':
			return {
				code,
				title: `${ctx.environmentName ?? 'The environment'} is archived.`,
				body: 'Archived environments keep their history but accept no changes.'
			};
	}
	return null;
}

/** A refused request (an ApiRequestError, usually 4xx). */
export function refusal(e: unknown, ctx: RefusalContext): Refusal {
	const v = errorView(e);
	return (
		forCode(v.code, v.message, ctx) ?? {
			code: v.code,
			title: `${ctx.name} couldn't be ${PAST[ctx.verb]}.`,
			body: sentence(v.message)
		}
	);
}

/** Guidance for registry failures of pulls and connection tests (#19). */
export function registryGuidance(errorClass: string | undefined): string | undefined {
	switch (errorClass) {
		case 'unauthorized':
			return 'The registry refused the credentials. Check or rotate the registry connection in Registries, or add one for this image.';
		case 'forbidden':
			return 'The account has no access to this repository. Use a token with pull access to it.';
		case 'rate_limited':
			return 'The registry limits pulls. Wait and try again; an authenticated connection raises the limit (Docker Hub still limits signed-in accounts).';
		case 'not_found':
			return 'The registry has no such image or tag. Check the reference; private images also answer this without access.';
		case 'registry_unavailable':
			return "The registry didn't answer. Try again later.";
		case 'credential_unavailable':
			return 'The registry connection this job used was removed or revoked. Pick another connection or rotate its credential.';
		case 'ambiguous_registry_connection':
			return 'Several registry connections match this image equally well. Choose one.';
		case 'registry_connection_revoked':
			return 'The matching registry connection is revoked; Docker Manager never falls back to anonymous pulls. Rotate its credential or remove it.';
	}
	return undefined;
}

/** A job that ended without succeeding (failed, partial, cancelled, interrupted). */
export function jobFailure(job: Job, ctx: RefusalContext): Refusal {
	const cls = job.error?.class;
	const message = sentence(job.error?.message ?? '');
	const byCode = forCode(cls, message, ctx);
	if (byCode) return byCode;
	const guidance = registryGuidance(cls);
	if (job.state === 'cancelled')
		return { code: cls, title: `${progressTitle(ctx.verb, ctx.name)} was cancelled.` };
	return {
		code: cls,
		title: `${ctx.name} couldn't be ${PAST[ctx.verb]}.`,
		body: [message, guidance ?? job.error?.recovery].filter(Boolean).join(' ')
	};
}
