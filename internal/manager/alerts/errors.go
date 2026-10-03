package alerts

import "strings"

// Failure words: what an error class means and what to do about it, in
// the words of a message. A job's error message and recovery text never
// reach a message (they can hold paths, addresses or a service's own
// words): only its class does, explained here. A class without words
// says only that a step failed.

// errorWords explains an error class: what went wrong, and what to do.
type errorWords struct{ what, fix string }

var errorTexts = map[string]errorWords{
	// The job engine.
	"agent_offline":         {"The environment's agent was offline, so the job never started.", "Check that the agent is running and connected, then run it again."},
	"authorization_revoked": {"The permission to run it was withdrawn before it started.", "Run it again as someone who still has the permission."},
	"unknown_outcome":       {"The agent stopped during a step that cannot be repeated safely, so it is unknown whether that step finished.", "Check the result before you run it again."},
	"journal_lost":          {"The agent lost track of the job (it restarted without its record of it).", "Run it again."},
	"resume_limit":          {"It was interrupted too often to resume.", "Check why the agent or manager keeps restarting, then run it again."},
	"rejected":              {"The agent refused the job: its input was not valid for this agent.", "Update the agent to the manager's version, then run it again."},
	"compensation_failed":   {"It failed, and putting things back afterwards (restarting stopped containers) failed too.", "Open the job, check which containers are stopped and start them."},
	"executor_restarted":    {"The agent or manager restarted during the job and it could not resume.", "Run it again."},
	"credential_unavailable": {"A credential it needs (a repository's key, the Recovery Key or a registry login) is missing or was revoked.",
		"Check the repository or registry connection in Docker Manager, then run it again."},
	"policy_rejected":    {"Its policy no longer allowed the run (turned off, deleted, or its target is gone).", "Check the policy."},
	"target_moved":       {"The stack moved to another environment before the job started.", "Run it again on the stack's new environment."},
	"internal":           {"Docker Manager could not record the job's progress.", "Check the manager's disk space and logs, then run it again."},
	"step_failed":        {"A step failed.", "Open the job to see which step failed and why."},
	"cancelled":          {"It was cancelled.", ""},
	"engine_error":       {"The Docker Engine reported an error.", "Open the job to see the Engine's answer."},
	"engine_unavailable": {"The Docker Engine could not be reached on the host.", "Check that Docker is running on the host."},

	// Backup storage (restic).
	"repository_not_found":       {"No backup repository was found at the repository's location.", "Check the repository's location (bucket, prefix or path) in Docker Manager."},
	"recovery_key_rejected":      {"The repository did not accept the Recovery Key.", "Check that the repository belongs to this Docker Manager and that a key change finished."},
	"repository_locked":          {"Another backup, restore or cleanup was using the repository.", "Wait for it to finish; the next run will go ahead."},
	"storage_access_denied":      {"The storage refused the repository's credentials.", "Check the access key, secret and bucket permissions of the repository."},
	"storage_unreachable":        {"The backup storage could not be reached (DNS, network or TLS).", "Check that the host can reach the storage endpoint."},
	"repository_damaged":         {"The repository check found damaged or missing data.", "Run a repository check, and make a fresh backup to another repository if it fails again."},
	"snapshot_not_found":         {"The snapshot, or a path in it, no longer exists.", "Pick another snapshot."},
	"restic_unavailable":         {"The backup program (restic) is missing on the host.", "Update the agent: it brings restic with it."},
	"restic_failed":              {"The backup program (restic) failed.", "Open the job to see restic's answer."},
	"repository_not_usable":      {"This repository cannot hold Docker Manager's own backup.", "Choose another repository for the manager's backup."},
	"recovery_key_not_confirmed": {"The Recovery Key has not been confirmed yet.", "Confirm the Recovery Key under Backups, then run it again."},

	// Backups and restores on the host.
	"empty_scope":           {"There was nothing to back up: every stack or volume of the policy is gone or excluded.", "Check the policy's stacks and volumes."},
	"shutdown_failed":       {"Stopping the stack's containers before the backup failed, so nothing was backed up.", "Check the stack's containers, then run it again."},
	"restart_failed":        {"The data was saved, but starting the stopped containers again failed.", "Start the stack's containers."},
	"files_unreadable":      {"Some files could not be read and are missing from the snapshot.", "Check the files' permissions on the host."},
	"item_gone":             {"A stack or volume was removed before its turn.", ""},
	"invalid_scope":         {"A stack or volume could not be backed up as configured.", "Check the policy's stacks, volumes and excluded paths."},
	"volume_unavailable":    {"A volume could not be backed up (it is blocked, excluded or of an unsupported kind).", "Check the volume."},
	"forbidden_path":        {"The stack's folder is not verified on the host.", "Check the environment's storage folders."},
	"snapshot_path_unknown": {"The snapshot does not contain this stack, volume or path.", "Pick another snapshot."},
	"target_missing":        {"The volume to restore into does not exist on the host.", "Create the volume or restore the whole stack."},
	"path_not_restorable":   {"A path could not be restored (outside the stack, a link leading elsewhere, or another file type).", "Restore a different path."},
	"restore_blocked":       {"Running or protected containers blocked the restore.", "Stop the containers it names, then restore again."},
	"insufficient_space":    {"There is not enough free disk space for the restore.", "Free some disk space on the host."},
	"restore_failed":        {"Putting the restored data in place failed; the previous data was put back.", "Open the job to see what failed."},

	// Updates.
	"source_changed":                {"The stack's Compose file changed since the update was planned.", "Check for updates again, then update."},
	"candidate_changed":             {"The image tag points to another version than the one the check found.", "Check for updates again."},
	"container_recreated":           {"The container was replaced, renamed or excluded from updates since the check.", "Check for updates again."},
	"unhealthy":                     {"The updated service did not become healthy.", "Open the stack's logs; pin the previous image if it keeps failing."},
	"service_exited":                {"The updated service stopped right after it started.", "Open the stack's logs; pin the previous image if it keeps failing."},
	"timeout":                       {"It did not finish in time.", "Run it again; check the host's load if it keeps timing out."},
	"no_containers":                 {"No containers were running after the update.", "Open the stack and start it."},
	"dependency_failed":             {"A service it depends on failed.", "Check the stack's other services."},
	"dependency_missing":            {"A service it depends on is missing.", "Check the stack's Compose file."},
	"dependency_conflict":           {"The update would have had to start services that are stopped.", "Start the stack, or update it by hand."},
	"protected":                     {"It would have changed Docker Manager's own container.", "Update Docker Manager as its documentation says."},
	"unauthorized":                  {"The registry refused the credentials.", "Check the registry connection's username and token."},
	"forbidden":                     {"The registry denied access to the image.", "Check that the registry login may pull this image."},
	"rate_limited":                  {"The registry's rate limit was reached.", "Wait, or add a registry login to raise the limit."},
	"registry_unavailable":          {"The registry could not be reached or failed.", "Try again later."},
	"not_found":                     {"The image or tag does not exist in the registry.", "Check the image name and tag."},
	"platform_not_found":            {"The registry has no image for the host's platform.", "Use an image that supports the host's architecture."},
	"invalid_response":              {"The registry answered with something unexpected.", "Check the registry's address."},
	"invalid_reference":             {"An image reference is not valid.", "Check the image names in the Compose file."},
	"invalid_project":               {"The stack's Compose file could not be loaded.", "Fix the Compose file."},
	"build_failed":                  {"Building an image failed.", "Open the job to see the build output."},
	"ambiguous_registry_connection": {"Several registry connections match the image equally.", "Pick one in the update policy."},
	"registry_connection_revoked":   {"The registry connection was revoked.", "Pick another registry connection."},
	"policy_not_found":              {"The update policy was deleted.", ""},
	"update_target_ineligible":      {"The stack or container is excluded from updates.", ""},
	"target_not_found":              {"The stack was removed.", ""},
	"environment_offline":           {"The environment was offline.", "Check that the agent is running and connected."},

	// Prune.
	"invalid_input": {"The agent could not read the prune's settings.", "Update the agent to the manager's version."},
}

// describeError explains an error class in one or two sentences ("" for
// none). Unknown classes are named as they are (they are stable words,
// never an error text).
func describeError(class string) string {
	if class == "" {
		return ""
	}
	w, ok := errorTexts[class]
	if !ok {
		return "It failed (" + strings.ReplaceAll(class, "_", " ") + ")."
	}
	if w.fix == "" {
		return w.what
	}
	return w.what + " " + w.fix
}

// errorReason is the short "what went wrong" of an error class.
func errorReason(class string) string {
	if w, ok := errorTexts[class]; ok {
		return w.what
	}
	return ""
}

// errorFix is "what to do" about an error class.
func errorFix(class string) string { return errorTexts[class].fix }
