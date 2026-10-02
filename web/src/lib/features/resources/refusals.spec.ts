import { describe, expect, it } from 'vitest';
import { ApiRequestError, type Job } from '$lib/api/client';
import {
	doneTitle,
	jobFailure,
	plain,
	protectionLabel,
	refusal,
	RefusalError,
	registryGuidance,
	sentence
} from './refusals';

const apiError = (status: number, code: string, message: string) =>
	new ApiRequestError(message, status, {
		code,
		message,
		requestId: 'r1',
		retryable: false,
		details: []
	});

const agent = {
	role: 'agent' as const,
	reason: 'the Docker Agent connected to this environment: stopping or removing it cuts Docker Manager off from this host',
	self: true,
	restartAllowed: false
};

describe('refusals (#32: a clear reason for every refused action)', () => {
	it("says that Docker Manager's own agent can't be stopped, with the server's reason", () => {
		const r = refusal(
			apiError(
				409,
				'protected',
				`refused to stop a protected Docker Manager resource: ${agent.reason}`
			),
			{ kind: 'container', name: 'docker-agent', verb: 'stop', protection: agent }
		);
		expect(r.code).toBe('protected');
		expect(r.title).toBe("Docker Manager's own agent can't be stopped from Docker Manager.");
		expect(r.body).toContain('Refused to stop a protected Docker Manager resource');
		expect(r.body).toContain('cuts Docker Manager off from this host');
		expect(r.body).toContain('use Docker on the host');
	});

	it('names the stacks volume and the manager', () => {
		const stacks = {
			role: 'stacks' as const,
			reason: 'the Docker Manager stacks volume (#28)',
			self: false,
			restartAllowed: false
		};
		expect(
			refusal(apiError(409, 'protected', 'x'), {
				kind: 'volume',
				name: 'docker-manager_stacks',
				verb: 'remove',
				protection: stacks
			}).title
		).toBe("The stacks volume can't be removed from Docker Manager.");
		expect(protectionLabel(stacks)).toBe('The Docker Manager Stacks Volume');
		expect(protectionLabel({ ...agent, role: 'manager' })).toBe('The Docker Manager');
	});

	it('explains managed stacks, running containers, offline environments and unknown codes', () => {
		const ctx = {
			kind: 'container' as const,
			name: 'silo-web',
			verb: 'remove' as const,
			environmentName: 'homelab'
		};
		expect(refusal(apiError(409, 'stack_managed', 'x'), ctx).title).toBe(
			'silo-web belongs to a stack Docker Manager manages.'
		);
		expect(refusal(apiError(409, 'container_running', 'x'), ctx).title).toBe(
			'silo-web is running.'
		);
		const off = refusal(apiError(503, 'environment_offline', 'x'), ctx);
		expect(off.title).toBe('homelab is offline.');
		expect(off.body).toContain("try again when it's back");
		const other = refusal(apiError(500, 'internal', 'boom'), ctx);
		expect(other.title).toBe("silo-web couldn't be removed.");
		expect(other.body).toBe('Boom.');
	});

	it('turns failed jobs into what happened and what to do (#19 registry classes)', () => {
		const job = {
			id: 'j1',
			state: 'failed',
			error: {
				class: 'rate_limited',
				message: 'toomanyrequests: pull rate limit',
				recovery: 'Wait.'
			}
		} as Job;
		const r = jobFailure(job, { kind: 'image', name: 'nginx:1.27', verb: 'pull' });
		expect(r.title).toBe("nginx:1.27 couldn't be pulled.");
		expect(r.body).toContain('Toomanyrequests: pull rate limit.');
		expect(r.body).toContain('Docker Hub still limits signed-in accounts');
		const refused = jobFailure(
			{ ...job, error: { class: 'protected', message: 'refused', recovery: '' } } as Job,
			{ kind: 'container', name: 'docker-agent', verb: 'stop', protection: agent }
		);
		expect(refused.title).toBe(
			"Docker Manager's own agent can't be stopped from Docker Manager."
		);
		expect(
			jobFailure({ ...job, state: 'cancelled' } as Job, {
				kind: 'image',
				name: 'x',
				verb: 'pull'
			}).title
		).toBe('Pulling x was cancelled.');
		expect(registryGuidance('unauthorized')).toContain('rotate the registry connection');
		expect(registryGuidance('nope')).toBeUndefined();
	});

	it('repeats the action in the done title and carries refusals through errors', () => {
		expect(doneTitle('stop', 'silo-web')).toBe('Stopped silo-web');
		expect(doneTitle('pull', 'nginx:1.27')).toBe('Pulled nginx:1.27');
		const e = new RefusalError({ title: 'A.', body: 'B.' });
		expect(e.message).toBe('A. B.');
		expect(e.refusal.title).toBe('A.');
	});

	it('drops roadmap references from server text', () => {
		expect(plain("every stack's project files (#28)")).toBe("every stack's project files");
		expect(plain('restore it from a backup, #10).')).toBe('restore it from a backup).');
		expect(sentence('the stacks volume (#28, #32)')).toBe('The stacks volume.');
		expect(sentence('')).toBe('');
	});
});
