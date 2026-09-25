package api

import "net/http"

// ErrorCode documents one stable error code of the public API. The catalog
// below is the source of docs/api/errors.md (TestErrorCatalogDocumented) and
// every code literal used in this package must be listed here
// (TestErrorCodesCatalogued). Feature workstreams add their specific codes
// (e.g. stack_name_taken) to this list in the same change that introduces
// them. Codes are never renamed or reused for a different meaning.
type ErrorCode struct {
	Code string
	// Status is the HTTP status the code is returned with.
	Status int
	// Retryable is the default retryable flag of the code.
	Retryable bool
	// Meaning is the one-line description published in docs/api/errors.md.
	Meaning string
	// Owner is the issue that introduced the code.
	Owner int
}

// ErrorCodes returns the catalog of stable error codes, grouped by status.
func ErrorCodes() []ErrorCode {
	return []ErrorCode{
		{CodeBadRequest, http.StatusBadRequest, false, "The request is malformed (unparsable JSON, wrong content encoding).", 2},
		{CodeInvalidCode, http.StatusBadRequest, false, "The one-time code (invitation, password reset or owner recovery) is unknown, expired, revoked or already used. The response never says which.", 16},
		{CodeUnauthenticated, http.StatusUnauthorized, false, "No valid session cookie or API token; sign in again or send a valid bearer token.", 2},
		{CodeInvalidCredentials, http.StatusUnauthorized, false, "Sign-in, second factor or step-up failed: unknown account, wrong password or code, disabled account and bad passkey assertions all look alike.", 16},
		{CodeForbidden, http.StatusForbidden, false, "Authenticated, but the named capability is not granted for this resource. Returned only when the caller may know the resource exists; otherwise not_found.", 2},
		{CodeInsecureOrigin, http.StatusForbidden, false, "The request did not reach DockYard over HTTPS on DOCKYARD_PUBLIC_URL (first-run setup); the message explains how to fix the proxy or URL.", 16},
		{CodeCrossOriginRequest, http.StatusForbidden, false, "A browser sent an unsafe request from another origin (cross-site request forgery protection).", 16},
		{CodeStepUpRequired, http.StatusForbidden, false, "The change needs recent authentication; re-authenticate with POST /api/v1/auth/step-ups and retry.", 16},
		{CodeEnrollmentRequired, http.StatusForbidden, false, "The session may only enroll the sign-in factors the instance policy requires; finish enrollment first.", 16},
		{CodeEnrollmentExpired, http.StatusForbidden, false, "The grace period to enroll required sign-in factors has passed; ask the instance owner for a factor or password reset.", 16},
		{CodeAPITokenNotAllowed, http.StatusForbidden, false, "API tokens cannot call this operation: owner administration, sign-in and factor flows, token management and Recovery Key administration need a signed-in browser session.", 31},
		{CodeAPITokensDisabled, http.StatusForbidden, false, "The instance owner disabled API tokens (security settings); no token can be created or used.", 31},
		{CodeSignInMethodNotAllowed, http.StatusForbidden, false, "The instance sign-in policy does not accept this sign-in method (for example a passkey when password and TOTP are required).", 16},
		{CodeNotFound, http.StatusNotFound, false, "The resource or route does not exist, or the caller may not know that it exists.", 2},
		{CodeMethodNotAllowed, http.StatusMethodNotAllowed, false, "The route exists but not with this method; see the Allow header.", 2},
		{CodeNotAcceptable, http.StatusNotAcceptable, false, "The Accept header excludes every media type the route can produce.", 2},
		{CodeConflict, http.StatusConflict, false, "Generic conflict with the current state. Routes prefer a specific 409 code.", 2},
		{CodeJobFinished, http.StatusConflict, false, "The job already reached a terminal state (for example a cancellation of a finished job).", 26},
		{CodeIdempotencyKeyReused, http.StatusConflict, false, "The Idempotency-Key was already used by this caller for a different request (different route, parameters or body).", 26},
		{CodeIdempotencyKeyInFlight, http.StatusConflict, true, "A request with the same Idempotency-Key is still being processed; retry after the Retry-After delay.", 4},
		{CodeSetupComplete, http.StatusConflict, false, "First-run setup already created the instance owner; sign in instead.", 16},
		{CodeUsernameTaken, http.StatusConflict, false, "Another account already uses this username.", 16},
		{CodeOwnerProtected, http.StatusConflict, false, "The instance owner cannot be disabled, deleted or reset through this route (use owner recovery).", 16},
		{CodeFactorRequired, http.StatusConflict, false, "Removing this factor would leave the account unable to satisfy the instance sign-in policy.", 16},
		{CodeTOTPAlreadyEnabled, http.StatusConflict, false, "TOTP is already enabled; remove it before enrolling a new secret.", 16},
		{CodeInvitationRedeemed, http.StatusConflict, false, "The invitation was already redeemed and can no longer be revoked.", 16},
		{CodeNoPendingFlow, http.StatusConflict, false, "No sign-in, TOTP enrollment or passkey ceremony is in progress in this session (or it expired); start again.", 16},
		{CodeEngineAlreadyEnrolled, http.StatusConflict, false, "Agent enrollment: the Docker Engine already has an active agent (one agent per Engine); enroll with intent replace:<agentId> to move to the new agent.", 3},
		{CodeEngineIdentityConflict, http.StatusConflict, false, "Agent enrollment: the Engine ID is already enrolled from another host (a cloned machine?); replace the agent, regenerate the clone's Engine ID or allow the duplicate Engine ID.", 3},
		{CodeEnvironmentArchived, http.StatusConflict, false, "The environment is archived: it cannot be edited, and enrolling its Engine needs intent reattach:<environmentId>.", 3},
		{CodeEnvironmentDetached, http.StatusConflict, false, "Agent enrollment: the Engine belongs to an environment whose agent was removed; enroll with intent reattach:<environmentId>.", 3},
		{CodeEngineMismatch, http.StatusConflict, false, "Agent enrollment: a replace or reattach enrollment was used for a different Docker Engine than its target's.", 3},
		{CodeEnrollmentTargetUnavailable, http.StatusConflict, false, "Agent enrollment: the agent to replace or the environment to re-attach is no longer in a state that allows it; create a new enrollment.", 3},
		{CodeAgentRevoked, http.StatusConflict, false, "The agent was removed or replaced; its credential cannot be rotated.", 3},
		{CodeGroupNameTaken, http.StatusConflict, false, "Another permission group already uses this name.", 17},
		{CodeDefaultGroupProtected, http.StatusConflict, false, "The default group cannot be deleted; make another group the default first.", 17},
		{CodeGroupNotEmpty, http.StatusConflict, false, "The group still has members; move them to another group first (users are never moved implicitly).", 17},
		{CodeRegistryNameTaken, http.StatusConflict, false, "Another registry connection already uses this name.", 19},
		{CodeAmbiguousRegistryConnection, http.StatusConflict, false, "Several registry connections match the image equally well (same host, repository matcher specificity, binding and priority); name one explicitly (registryId).", 19},
		{CodeGitCredentialNameTaken, http.StatusConflict, false, "Another Git credential already uses this name.", 33},
		{CodeAmbiguousGitCredential, http.StatusConflict, false, "Several Git credentials match the repository equally well (same host and path prefix length); name one explicitly (gitCredentialId).", 33},
		{CodeGitCredentialRevoked, http.StatusConflict, false, "The Git credential selected for the repository is revoked; DockYard never falls back to anonymous access. Set a new token or select another credential.", 33},
		{CodeBuildDefinitionNameTaken, http.StatusConflict, false, "Another build definition in this environment already uses this name.", 33},
		{CodeMaintenancePolicyNameTaken, http.StatusConflict, false, "Another maintenance policy in this environment already uses this name.", 14},
		{CodeMaintenancePolicyEmpty, http.StatusConflict, false, "The maintenance policy has no enabled rule; enable at least one rule before running it.", 14},
		{CodeMaintenanceRunActive, http.StatusConflict, false, "A run of the maintenance policy is still queued or running; follow that job instead of starting another run.", 14},
		{CodePruneConfirmationRequired, http.StatusConflict, false, "A manual prune run deletes resources and cannot be undone: review a preview and repeat the request with confirm: true.", 14},
		{CodeRegistryConnectionRevoked, http.StatusConflict, false, "The registry connection selected for the image is revoked; DockYard never falls back to anonymous access. Rotate a new credential into it or select another connection.", 19},
		{CodeStackManaged, http.StatusConflict, false, "The container, volume or network belongs to a DockYard-managed stack: change the stack's Compose definition (or use the stack's operations) instead of editing or removing it directly.", 6},
		{CodeContainerRunning, http.StatusConflict, false, "The container is running; stop it first or remove it with force=true.", 6},
		{CodeImageInUse, http.StatusConflict, false, "Containers (running or not) use the image; remove them first.", 6},
		{CodeVolumeInUse, http.StatusConflict, false, "Containers (running or not) mount the volume; remove them first.", 6},
		{CodeNetworkInUse, http.StatusConflict, false, "Containers are attached to the network; disconnect or remove them first.", 6},
		{CodeNetworkBuiltin, http.StatusConflict, false, "Predefined networks (bridge, host, none) cannot be removed.", 6},
		{CodeResourceNameTaken, http.StatusConflict, false, "Another container, volume or network of the environment already uses this name.", 6},
		{CodeProtected, http.StatusConflict, false, "The container, image, volume or network is one of DockYard's own (its agent, manager, data, stacks volume or deployment): the operation is refused for everyone, the owner included; use Docker on the host if you really must.", 32},
		{CodeConfirmationRequired, http.StatusConflict, false, "Restarting this container interrupts DockYard (its manager or deployment); repeat the request with confirm: true.", 32},
		{CodeUnsupportedAPIVersion, http.StatusConflict, false, "The environment's Docker Engine API version is too old for the operation; upgrade Docker Engine (25.0 or newer, see the support matrix).", 6},
		{CodeFileExists, http.StatusConflict, false, "File manager: the name already exists (choose overwrite, skip or keep both, or another name).", 15},
		{CodeFileConflict, http.StatusConflict, false, "File manager: the entry changed during the operation or the operation would put a directory into itself.", 15},
		{CodeFileTypeMismatch, http.StatusConflict, false, "File manager: the path is a directory where a file is needed, or a path component is not a directory.", 15},
		{CodeFileUnsupported, http.StatusConflict, false, "File manager: the entry's content is not served (a symlink, device, FIFO or socket, or a file with several hard links whose other names may lie outside the root).", 15},
		{CodeVolumeFilesUnsupported, http.StatusConflict, false, "File manager: this volume cannot be browsed (non-local driver or remote-backed local volume, DockYard's own volumes, the stacks volume, or the agent's storage layout is not verified); the message says why.", 15},
		{CodeStackNameTaken, http.StatusConflict, false, "The environment already has a DockYard stack with this Compose project name or project directory; nothing was overwritten.", 7},
		{CodeComposeProjectExists, http.StatusConflict, false, "The Docker Engine already runs a Compose project with this name that DockYard does not manage; import it instead of creating a new stack.", 7},
		{CodeStackDirectoryExists, http.StatusConflict, false, "The project directory already exists in the stacks volume; nothing was overwritten (import the project or choose another name).", 7},
		{CodeStackDefinitionChanged, http.StatusConflict, false, "The stack's definition on disk changed while the request ran (for example during a revision restore); reload and retry.", 7},
		{CodeStackNotAdoptable, http.StatusConflict, false, "The discovered Compose project cannot be adopted in place (its directory is outside the stacks volume and the registered stack roots, or its files are elsewhere); import it with an explicit Compose source.", 7},
		{CodeStackRootUnavailable, http.StatusConflict, false, "The agent refuses the stack's project directory: its storage layout is not verified, the root is not registered, or the directory is missing (#28).", 7},
		{CodeRevisionContentUnavailable, http.StatusConflict, false, "The revision was recorded by hash only (its definition was too large for a deploy result) and cannot be restored.", 7},
		{CodeMigrationBlocked, http.StatusConflict, false, "The migration's preflight check has blockers (details lists them: platform, name or port conflicts, missing external networks, free space, offline agents, ...); preview the migration, resolve them and retry.", 35},
		{CodeMigrationNotCompleted, http.StatusConflict, false, "The source of a stack migration can be removed only after the migration completed.", 35},
		{CodeMigrationSourceRemoved, http.StatusConflict, false, "The migration's source was already removed.", 35},
		{CodeMigrationSourceInUse, http.StatusConflict, false, "A DockYard stack on the source environment manages the migrated project again (it was imported back); its files are not removed.", 35},
		{CodeUpdatePolicyTargetUsed, http.StatusConflict, false, "The stack or container already has an update policy (one per target); edit that policy.", 20},
		{CodeUpdatePolicyNameTaken, http.StatusConflict, false, "Another update policy in the environment already uses this name.", 20},
		{CodeUpdateTargetIneligible, http.StatusConflict, false, "The target cannot follow digests: DockYard's own project or containers (#32), a container without a saved recreate specification, or a stack member; the message says which.", 20},
		{CodeNoUpdateCandidates, http.StatusConflict, false, "Nothing to update: no checked candidate with a new host-platform digest (run a check first; quarantined and failed candidates are not applied).", 20},
		{CodeUpdateSourceDrift, http.StatusConflict, false, "The stack's definition on disk differs from the applied revision (undeployed changes); deploy it first. An update never deploys an edit or writes a file.", 20},
		{CodeUpdatePreviewStale, http.StatusConflict, false, "The candidates, digests or the stack's definition changed since the given preview; preview again.", 20},
		{CodeBackupRepositoryNameTaken, http.StatusConflict, false, "Another backup repository already uses this name.", 10},
		{CodeBackupPolicyNameTaken, http.StatusConflict, false, "Another backup policy already uses this name.", 10},
		{CodeBackupRepositoryInUse, http.StatusConflict, false, "A backup policy uses the repository; change or delete the policy first.", 10},
		{CodeRecoveryKeyNotConfirmed, http.StatusConflict, false, "The Recovery Key has not been confirmed (re-entered) for the repository yet; confirm it before policies can use or enable it.", 10},
		{CodeKeyRotationInProgress, http.StatusConflict, false, "A Recovery Key rotation is still moving repository locations to the new key; wait until no location is pending.", 10},
		{CodeNothingToRetry, http.StatusConflict, false, "Every member of the backup set completed; there is nothing to retry.", 10},
		{CodeBackupRepositoryError, http.StatusConflict, false, "The backup repository could not be read (missing, Recovery Key rejected, storage refused access, locked or damaged); the message names the class and what to do.", 10},
		{CodeBackupNotAFile, http.StatusConflict, false, "Only regular files can be downloaded from a backup (not directories, links or special files).", 10},
		{CodeManagerRestoreRequired, http.StatusConflict, false, "Manager-state backups are not restored like stack or volume data: import them into a fresh manager (first-run setup, backup import), which replaces the whole manager state.", 10},
		{CodeBackupImportSchemaIncompatible, http.StatusConflict, false, "The backup set was written by a newer DockYard whose database this build cannot run; install at least that version and import again.", 24},
		{CodeBackupImportStateMissing, http.StatusConflict, false, "The backup set has no readable manager state (missing manager repository or snapshot, damaged secret-key bundle or database); choose another set. Host-only recovery is documented.", 24},
		{CodeBackupImportInProgress, http.StatusConflict, false, "A backup import is already running on this manager; follow it in the setup status.", 24},
		{CodeGone, http.StatusGone, false, "The resource existed but was removed permanently (for example an expired invitation).", 2},
		{CodeLengthRequired, http.StatusLengthRequired, false, "Uploads need a Content-Length header.", 15},
		{CodePreconditionFailed, http.StatusPreconditionFailed, false, "If-Match does not name the current revision. The response carries the current ETag; refetch, merge and retry.", 4},
		{CodePayloadTooLarge, http.StatusRequestEntityTooLarge, false, "The request body exceeds the route's documented limit.", 2},
		{CodeBackupFileTooLarge, http.StatusRequestEntityTooLarge, false, "The file in the backup is larger than the download limit (2 GiB); restore it instead.", 10},
		{CodeUnsupportedMediaType, http.StatusUnsupportedMediaType, false, "The Content-Type is not accepted by the route.", 2},
		{CodeRangeNotSatisfiable, http.StatusRequestedRangeNotSatisfiable, false, "The Range of a single-file download lies outside the file; Content-Range carries its size.", 15},
		{CodeValidationFailed, http.StatusUnprocessableEntity, false, "One or more inputs are invalid; details lists each field.", 2},
		{CodeRecreateRequired, http.StatusUnprocessableEntity, false, "The requested container settings cannot change in place; create a new container (or use a Compose stack). details lists the fields.", 6},
		{CodeContentDigestMismatch, http.StatusUnprocessableEntity, false, "The uploaded bytes do not match X-DockYard-Content-SHA256; nothing was written.", 15},
		{CodeBackupImportKeyRejected, http.StatusUnprocessableEntity, false, "The Recovery Key does not open the manager repository. Check it for typos; after a rotation also enter the previous key. A lost Recovery Key cannot be recovered: nobody can decrypt the backups.", 24},
		{CodeBackupImportNotFound, http.StatusUnprocessableEntity, false, "No DockYard repository (or no such backup set) at the import destination; check endpoint, bucket, prefix or the mounted path.", 24},
		{CodeBackupImportManifestCorrupt, http.StatusUnprocessableEntity, false, "The portable manifest of the backup set is damaged (truncated or checksum mismatch); choose another set or check the repository.", 24},
		{CodeBackupImportKeyRotated, http.StatusUnprocessableEntity, false, "The set's manager state is sealed under another Recovery Key (a rotation happened after it); enter the newest key and the previous one.", 24},
		{CodeBackupImportUnreachable, http.StatusUnprocessableEntity, false, "The import destination could not be read (storage refused access, unreachable, locked or damaged); the message names the class and what to do.", 24},
		{CodeInvalidDefinition, http.StatusUnprocessableEntity, false, "The Compose definition does not validate (syntax, paths or unsupported features); details lists each finding.", 7},
		{CodeDefinitionTooLarge, http.StatusUnprocessableEntity, false, "The Compose definition exceeds its bounds (256 KiB per file, 512 KiB and 32 files in total).", 7},
		{CodeRecoveryKeyMismatch, http.StatusUnprocessableEntity, false, "The re-entered Recovery Key is well-formed but is not the instance's (pending or current) key.", 10},
		{CodeRecoveryKeyMalformed, http.StatusUnprocessableEntity, false, "The Recovery Key has a typo: its length or checksum is wrong (DYRK- followed by 13 groups of four characters).", 10},
		{CodeVersionUnsupported, http.StatusUpgradeRequired, false, "Agent routes: the agent's protocol or version is outside the manager's window (same or previous minor release, never newer than the manager); upgrade as the message says.", 3},
		{CodePreconditionRequired, http.StatusPreconditionRequired, false, "The edit requires an If-Match header with the resource's current ETag.", 4},
		{CodeRateLimited, http.StatusTooManyRequests, true, "Too many requests; retry after the Retry-After delay.", 2},
		{CodeInternal, http.StatusInternalServerError, false, "Unexpected server error. The cause is logged under the request ID and never returned.", 2},
		{CodeEngineError, http.StatusBadGateway, true, "The environment's Docker Engine failed the operation; the message carries its explanation. Retrying helps only when the cause was transient.", 6},
		{CodeNotImplemented, http.StatusNotImplemented, false, "The route is declared but this manager build does not implement it yet.", 2},
		{CodeAgentUnsupported, http.StatusNotImplemented, false, "The environment's agent does not support this operation (it is older than the manager); upgrade the agent.", 6},
		{CodeJobKindUnavailable, http.StatusNotImplemented, false, "The operation would start a job kind whose executor this manager or agent does not provide yet.", 26},
		{CodeUnavailable, http.StatusServiceUnavailable, true, "A dependency (database, job engine, agent) is temporarily unavailable.", 2},
		{CodeNotReady, http.StatusServiceUnavailable, true, "Readiness check failed; details lists each failing check.", 2},
		{CodeEnvironmentOffline, http.StatusServiceUnavailable, true, "The environment's agent is not connected; retry when the environment is online again.", 6},
		{CodeEngineUnavailable, http.StatusServiceUnavailable, true, "The environment's agent is connected but cannot reach its Docker Engine.", 6},
		{CodeTimeout, http.StatusGatewayTimeout, true, "The operation did not finish within its deadline (also 408 for slow request bodies).", 2},
	}
}

// LookupErrorCode returns the catalog entry of code.
func LookupErrorCode(code string) (ErrorCode, bool) {
	for _, c := range ErrorCodes() {
		if c.Code == code {
			return c, true
		}
	}
	return ErrorCode{}, false
}
