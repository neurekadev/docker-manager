package api

import (
	"strconv"
	"strings"
)

// Conditional requests (#4). See docs/api/conventions.md.
//
// Revisioned resources expose their revision as a strong, quoted ETag on GET
// (embed ETagHeader in the output) and in the resource body (revision
// field). Edits (PATCH/PUT/DELETE of revisioned resources) embed IfMatchParam
// and call CheckIfMatch with the current ETag before changing anything:
//
//	if err := in.CheckIfMatch(api.RevisionETag(stack.Revision)); err != nil {
//		return nil, err // 428 without If-Match, 412 + current ETag on mismatch
//	}
//
// The comparison and the write must happen in one store transaction (or the
// store must compare-and-swap on the revision) so two concurrent edits
// cannot both pass.

// IfMatchParam is embedded in inputs of revision-checked edits.
type IfMatchParam struct {
	IfMatch string `header:"If-Match" maxLength:"1024" doc:"ETag of the revision being edited (from the resource's ETag header). Required: edits without it fail with 428 precondition_required; a stale value fails with 412 precondition_failed and the current ETag."`
}

// CheckIfMatch requires If-Match and compares it with the current ETag.
func (p IfMatchParam) CheckIfMatch(current string) error {
	return CheckIfMatch(p.IfMatch, current, true)
}

// ETagHeader is embedded in outputs of revisioned resources (GET and
// successful edits) so clients always hold the latest ETag.
type ETagHeader struct {
	ETag string `header:"ETag" doc:"Strong entity tag of the returned revision; send it as If-Match when editing."`
}

// ETag formats a revision identifier as a strong entity tag. Revisions must
// not contain double quotes or control characters.
func ETag(revision string) string { return `"` + revision + `"` }

// RevisionETag formats an integer revision counter as an entity tag.
func RevisionETag(revision int64) string { return ETag(strconv.FormatInt(revision, 10)) }

// CheckIfMatch evaluates an If-Match header value against the current ETag
// with RFC 9110 strong comparison: "*" matches any existing resource, a list
// matches when any strong tag equals current, weak tags never match.
//
//   - current == "" means the resource does not exist: only an absent header
//     passes (and only when not required).
//   - missing header: nil unless required (428 precondition_required).
//   - mismatch: 412 precondition_failed with the current ETag in the ETag
//     response header and a header.If-Match detail.
func CheckIfMatch(ifMatch, current string, required bool) error {
	ifMatch = strings.TrimSpace(ifMatch)
	if ifMatch == "" {
		if required {
			return PreconditionRequired("this edit requires an If-Match header",
				Field("header.If-Match", "send the ETag of the revision you are editing"))
		}
		return nil
	}
	if current != "" && matchesETag(ifMatch, current) {
		return nil
	}
	e := PreconditionFailed("the resource was changed since you loaded it",
		Field("header.If-Match", "stale revision; reload the resource, reapply your change and retry with its current ETag"))
	if current != "" {
		e = e.WithHeader("ETag", current)
	}
	return e
}

func matchesETag(header, current string) bool {
	if header == "*" {
		return true
	}
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if strings.HasPrefix(tag, "W/") {
			continue // weak tags never match under strong comparison
		}
		if tag == current {
			return true
		}
	}
	return false
}
