package api

// Conditional requests and idempotency (#4). These are the shared input types
// feature routes embed; the #4 workstream completes the helpers and tests.
//
// Edits (PATCH/PUT/DELETE of revisioned resources) embed IfMatchParam and
// compare it with the resource's current ETag, returning PreconditionFailed
// on mismatch and a 428 precondition_required when the header is missing on
// routes that demand it. GET responses of revisioned resources set an ETag
// header derived from the stored revision (strong, quoted).
//
// Dangerous retries (deploys, restores, deletions that start jobs) embed
// IdempotencyKeyParam. The manager stores (key, caller, route, request hash)
// → response for 24h: a replay with the same hash returns the stored
// response; a different hash under the same key is a 422.
//
// TODO(#4): ETag helper, idempotency store, 202 + job link response type.

// IfMatchParam is embedded in inputs of revision-checked edits.
type IfMatchParam struct {
	IfMatch string `header:"If-Match" doc:"ETag of the revision being edited."`
}

// IdempotencyKeyParam is embedded in inputs of operations safe to retry.
type IdempotencyKeyParam struct {
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128" doc:"Client-generated key making retries of this request safe."`
}
