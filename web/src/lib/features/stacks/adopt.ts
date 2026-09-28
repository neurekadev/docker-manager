// The stack page's running jobs after a reload (docs/internal/web.md, "Job
// progress after reload"): which running jobs belong to the stack's tray
// and the words the tray uses for a job it did not start itself. Pure;
// tested in adopt.spec.ts.
import type { JobMatch } from '$lib/features/jobs/active';
import { jobKindLabel } from '$lib/features/jobs/labels';

/**
 * The stack's jobs the tray shows: every kind acting on it but update
 * checks, and but migrations while the migration wizard (which shows them
 * itself) is open.
 */
export function stackTrayMatch(stackId: string, o: { wizard?: boolean } = {}): JobMatch {
	return {
		targets: [{ type: 'stack', id: stackId }],
		excludeKinds: o.wizard ? ['update.check', 'stack.migrate'] : ['update.check']
	};
}

/** What runs, the success toast and the failure toast ("Deploy Silo"). */
export interface StackJobCopy {
	title: string;
	success: string;
	failure: string;
}

const COPY: Record<string, (t: string) => StackJobCopy> = {
	'stack.deploy': (t) => ({
		title: `Deploy ${t}`,
		success: `Deployed ${t}`,
		failure: `${t} was not deployed`
	}),
	'stack.start': (t) => ({
		title: `Start ${t}`,
		success: `Started ${t}`,
		failure: `${t} was not started`
	}),
	'stack.stop': (t) => ({
		title: `Stop ${t}`,
		success: `Stopped ${t}`,
		failure: `${t} was not stopped`
	}),
	'stack.restart': (t) => ({
		title: `Restart ${t}`,
		success: `Restarted ${t}`,
		failure: `${t} was not restarted`
	}),
	'stack.pull': (t) => ({
		title: `Pull images of ${t}`,
		success: `Pulled images of ${t}`,
		failure: `The images of ${t} were not pulled`
	}),
	'stack.build': (t) => ({
		title: `Build images of ${t}`,
		success: `Built images of ${t}`,
		failure: `The images of ${t} were not built`
	}),
	'stack.update': (t) => ({
		title: `Update ${t}`,
		success: `Updated ${t}`,
		failure: `${t} was not updated`
	}),
	'stack.down': (t) => ({
		title: `Take down ${t}`,
		success: `Took down ${t}`,
		failure: `${t} was not taken down`
	}),
	'stack.remove': (t) => ({
		title: `Delete ${t}`,
		success: `Deleted ${t}`,
		failure: `${t} was not deleted`
	}),
	'stack.rename': (t) => ({
		title: `Rename ${t}`,
		success: `Renamed ${t}`,
		failure: `${t} was not renamed`
	}),
	'stack.migrate': (t) => ({
		title: `Migrate ${t}`,
		success: `Migrated ${t}`,
		failure: `${t} was not migrated`
	}),
	'stack.remove_source': (t) => ({
		title: `Remove ${t} from the old environment`,
		success: `Removed ${t} from the old environment`,
		failure: `${t} was not removed from the old environment`
	}),
	'restore.run': (t) => ({
		title: `Restore ${t}`,
		success: `Restored ${t}`,
		failure: `${t} was not restored`
	}),
	'backup.run': (t) => ({
		title: `Back up ${t}`,
		success: `Backed up ${t}`,
		failure: `${t} was not backed up`
	})
};

/** The tray's words for a job of `kind` on the stack called `title`. */
export function stackJobCopy(kind: string, title: string): StackJobCopy {
	const known = COPY[kind];
	if (known) return known(title);
	const what = jobKindLabel(kind);
	return {
		title: `${what}: ${title}`,
		success: `${what}: ${title} finished`,
		failure: `${what}: ${title} did not succeed`
	};
}
