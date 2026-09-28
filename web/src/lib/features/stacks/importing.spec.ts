import { describe, expect, it } from 'vitest';
import type { Schema } from '$lib/api/client';
import {
	asSentence,
	containerCounts,
	importConsequences,
	importDetails,
	importFailure,
	importHow,
	importMode,
	importNeedsConfirm,
	jobStackId,
	projectStatus,
	runningServices
} from './importing';

type Project = Schema<'DiscoveredStack'>;

function project(over: Partial<Project> = {}): Project {
	return {
		name: 'nextcloud',
		adoptable: false,
		copyable: true,
		services: [
			{ name: 'app', image: 'nextcloud:29', containers: 1, running: 1 },
			{ name: 'db', image: 'postgres:16', containers: 1, running: 1 },
			{ name: 'cron', image: 'nextcloud:29', containers: 1, running: 0 }
		],
		...over
	};
}

describe('importMode', () => {
	it('names the way a project is imported', () => {
		expect(importMode(project({ stackId: 's1', adoptable: true }))).toBe('managed');
		expect(importMode(project({ adoptable: true }))).toBe('adopt');
		expect(importMode(project({ copyable: false }))).toBe('blocked');
		expect(importMode(project({ protected: true }))).toBe('copy-live');
		expect(importMode(project())).toBe('copy-stop');
		expect(
			importMode(
				project({ services: [{ name: 'app', image: 'x', containers: 1, running: 0 }] })
			)
		).toBe('copy-stopped');
	});
});

describe('counts and status', () => {
	it('counts running services and containers', () => {
		expect(runningServices(project())).toBe(2);
		expect(containerCounts(project())).toEqual({ up: 2, total: 3 });
		expect(containerCounts(project({ services: [] }))).toEqual({ up: 0, total: 0 });
	});

	it('adds the containers up to one status', () => {
		expect(projectStatus({ up: 0, total: 3 })).toBe('stopped');
		expect(projectStatus({ up: 2, total: 3 })).toBe('partial');
		expect(projectStatus({ up: 3, total: 3 })).toBe('running');
	});
});

describe('importHow', () => {
	it('says in one line what happens, without paths', () => {
		expect(importHow(project({ adoptable: true }))).toBe(
			'Taken over where it is. Nothing restarts.'
		);
		expect(importHow(project({ protected: true }))).toBe(
			'Copied while it keeps running. Nothing stops.'
		);
		expect(importHow(project())).toBe(
			'Copied into Docker Manager. Its 2 running services stop briefly.'
		);
		expect(
			importHow(
				project({ services: [{ name: 'app', image: 'x', containers: 1, running: 1 }] })
			)
		).toBe('Copied into Docker Manager. Its 1 running service stops briefly.');
		expect(
			importHow(
				project({ services: [{ name: 'app', image: 'x', containers: 1, running: 0 }] })
			)
		).toBe('Copied into Docker Manager. It stays stopped.');
		expect(importHow(project({ copyable: false }))).toBe("Can't be imported as it is.");
		expect(importHow(project({ stackId: 's1' }))).toBe('Docker Manager manages it already.');
	});
});

describe('importConsequences and importNeedsConfirm', () => {
	it('lists what a copy that stops services does', () => {
		const c = importConsequences(project());
		expect(c[0]).toBe('Stops its 2 running services while the files are copied.');
		expect(c[2]).toBe('Starts the services again from the copy.');
		expect(c).toContain('Leaves the original folder untouched.');
		expect(importNeedsConfirm(project())).toBe(true);
	});

	it('asks nothing when nothing stops', () => {
		for (const p of [
			project({ adoptable: true }),
			project({ protected: true }),
			project({ copyable: false }),
			project({ stackId: 's1' }),
			project({ services: [{ name: 'app', image: 'x', containers: 1, running: 0 }] })
		]) {
			expect(importConsequences(p)).toEqual([]);
			expect(importNeedsConfirm(p)).toBe(false);
		}
	});
});

describe('importDetails', () => {
	it("explains a blocked project with the agent's reason and the usual fix", () => {
		const d = importDetails(
			project({ copyable: false, reason: 'the containers carry no Compose file label' })
		);
		expect(d[0]).toBe('The containers carry no Compose file label.');
		expect(d[1]).toContain('below /import');
	});

	it('has a fallback reason', () => {
		expect(importDetails(project({ copyable: false }))[0]).toBe(
			"Docker Manager can't read the project's folder."
		);
	});

	it('repeats the consequences of a stopping copy and the drift check', () => {
		const d = importDetails(project());
		expect(d.slice(0, 4)).toEqual(importConsequences(project()));
		expect(d[4]).toMatch(/^Before anything changes/);
	});

	it('is empty for managed projects', () => {
		expect(importDetails(project({ stackId: 's1' }))).toEqual([]);
	});
});

describe('jobStackId', () => {
	it("finds the job's stack target", () => {
		expect(
			jobStackId({
				targets: [
					{ type: 'volume', id: 'v' },
					{ type: 'stack', id: 'stack-1' }
				]
			})
		).toBe('stack-1');
		expect(jobStackId({ targets: [] })).toBeUndefined();
	});
});

describe('asSentence', () => {
	it('capitalises and ends with a stop', () => {
		expect(asSentence(' outside the stacks volume ')).toBe('Outside the stacks volume.');
		expect(asSentence('Done!')).toBe('Done!');
		expect(asSentence('')).toBe('');
	});
});

describe('importFailure', () => {
	it('says what happened and what to do', () => {
		expect(importFailure({ code: 'stack_name_taken', message: 'x' }, 'shop')).toBe(
			'Docker Manager already manages a stack named shop here. Nothing was changed.'
		);
		expect(importFailure({ code: 'stack_directory_exists', message: 'x' }, 'shop')).toBe(
			'Docker Manager already has a folder named shop. Nothing was overwritten: move that folder away, then import again.'
		);
		expect(
			importFailure({ code: 'stack_not_copyable', message: 'containers differ' }, 'shop')
		).toBe("shop can't be imported: containers differ");
		expect(importFailure({ code: 'agent_unsupported', message: 'x' }, 'shop')).toMatch(
			/Update the agent/
		);
		expect(importFailure({ message: 'The manager is unreachable.' }, 'shop')).toBe(
			'The manager is unreachable.'
		);
	});
});
