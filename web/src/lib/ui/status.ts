// Status vocabulary (#22): one mapping from API states to badge tone and
// plain-language label, shared by StatusBadge, tables and JobProgress.
import type { BadgeTone } from './Badge.svelte';

export interface StatusInfo {
	tone: BadgeTone;
	label: string;
	/** An in-progress state: the dot breathes (static under reduced motion). */
	pulse: boolean;
}

const STATUS: Record<string, StatusInfo> = {
	// Containers and stacks (Engine states, #6/#7).
	running: { tone: 'ok', label: 'Running', pulse: false },
	healthy: { tone: 'ok', label: 'Healthy', pulse: false },
	starting: { tone: 'info', label: 'Starting', pulse: true },
	restarting: { tone: 'warn', label: 'Restarting', pulse: true },
	unhealthy: { tone: 'danger', label: 'Unhealthy', pulse: false },
	paused: { tone: 'warn', label: 'Paused', pulse: false },
	stopped: { tone: 'neutral', label: 'Stopped', pulse: false },
	exited: { tone: 'neutral', label: 'Exited', pulse: false },
	created: { tone: 'neutral', label: 'Created', pulse: false },
	dead: { tone: 'danger', label: 'Dead', pulse: false },
	removing: { tone: 'warn', label: 'Removing', pulse: true },
	missing: { tone: 'neutral', label: 'Not running', pulse: false },
	partial: { tone: 'warn', label: 'Partially running', pulse: false },
	unknown: { tone: 'neutral', label: 'Unknown', pulse: false },
	// Stack deployment status (#7).
	deployed: { tone: 'ok', label: 'Deployed', pulse: false },
	undeployed: { tone: 'neutral', label: 'Not deployed', pulse: false },
	down: { tone: 'neutral', label: 'Down', pulse: false },
	// Environments (#3).
	online: { tone: 'ok', label: 'Online', pulse: false },
	offline: { tone: 'offline', label: 'Offline', pulse: false },
	archived: { tone: 'neutral', label: 'Archived', pulse: false },
	// Jobs (#26).
	queued: { tone: 'neutral', label: 'Queued', pulse: false },
	blocked: { tone: 'warn', label: 'Blocked', pulse: false },
	dispatched: { tone: 'info', label: 'Starting', pulse: true },
	cancelling: { tone: 'warn', label: 'Cancelling', pulse: true },
	succeeded: { tone: 'ok', label: 'Succeeded', pulse: false },
	failed: { tone: 'danger', label: 'Failed', pulse: false },
	cancelled: { tone: 'neutral', label: 'Cancelled', pulse: false },
	interrupted: { tone: 'danger', label: 'Interrupted', pulse: false },
	skipped: { tone: 'neutral', label: 'Skipped', pulse: false },
	// Updates (#20).
	update_available: { tone: 'warn', label: 'Update available', pulse: false },
	// Disk health (#143): disks (healthy, warning, failing, sleeping,
	// unreadable) and RAID arrays (healthy, degraded, rebuilding, checking,
	// failed, inactive).
	warning: { tone: 'warn', label: 'Warning', pulse: false },
	failing: { tone: 'danger', label: 'Failing', pulse: false },
	sleeping: { tone: 'neutral', label: 'Sleeping', pulse: false },
	unreadable: { tone: 'neutral', label: 'Unreadable', pulse: false },
	degraded: { tone: 'warn', label: 'Degraded', pulse: false },
	rebuilding: { tone: 'info', label: 'Rebuilding', pulse: true },
	checking: { tone: 'info', label: 'Checking', pulse: true },
	inactive: { tone: 'neutral', label: 'Inactive', pulse: false },
	// Alert severities (#159; warning is above): critical, warning, info.
	critical: { tone: 'danger', label: 'Critical', pulse: false },
	info: { tone: 'info', label: 'Info', pulse: false }
};

// Job "partial" differs from stack "partial": callers pass kind="job".
const JOB_OVERRIDES: Record<string, StatusInfo> = {
	partial: { tone: 'warn', label: 'Partly failed', pulse: false },
	running: { tone: 'info', label: 'Running', pulse: true }
};

export function statusInfo(status: string, kind: 'resource' | 'job' = 'resource'): StatusInfo {
	const key = status.toLowerCase();
	if (kind === 'job' && key in JOB_OVERRIDES) return JOB_OVERRIDES[key];
	return (
		STATUS[key] ?? {
			tone: 'neutral',
			label: key ? key[0].toUpperCase() + key.slice(1).replaceAll('_', ' ') : 'Unknown',
			pulse: false
		}
	);
}
