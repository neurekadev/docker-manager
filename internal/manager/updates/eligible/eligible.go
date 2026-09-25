// Package eligible decides whether an image definition can follow the
// digest behind its tag (#20). It is shared by the update service and the
// stack image status (#7), so both explain ineligibility the same way.
//
// Rules (in order; never inferred from tag text or image creation time):
//
//   - a service with a build section is built locally (#33): build_only;
//   - an unparsable reference: invalid_reference;
//   - a reference pinned by @sha256 is immutable: digest_pinned (the user
//     changes their own definition to follow a tag);
//   - a reference without an explicit tag ("nginx" implies latest):
//     untagged;
//   - a Compose pull_policy other than missing/if_not_present conflicts:
//     never and build keep the image local, always and the periodic
//     policies (daily, weekly, every_<duration>, refresh) pull on their own
//     at every up and would bypass the checked candidate and its
//     quarantine: pull_policy_conflict;
//   - otherwise eligible, with a warning flag for tags that do not read like
//     a version ("latest", "main"): they are eligible (#25) but can change
//     meaning.
package eligible

import (
	"fmt"
	"strings"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/imageref"
)

// Subject is what the rules look at.
type Subject struct {
	// Reference is the resolved image reference (after interpolation).
	Reference string
	// Build: the service has a build section.
	Build bool
	// PullPolicy is the Compose pull_policy ("" for standalone containers
	// and Compose's default).
	PullPolicy string
}

// Result is the decision.
type Result struct {
	Eligible bool
	// Reason is a domain.UpdateReason* code; Message explains it.
	Reason  string
	Message string
	// NonVersionTag warns about a moving tag name.
	NonVersionTag bool
	// Ref is the parsed reference (valid when Reason is not
	// invalid_reference).
	Ref imageref.Ref
}

// Check applies the rules.
func Check(s Subject) Result {
	if s.Build {
		return Result{Reason: domain.UpdateReasonBuildOnly,
			Message: "The service builds its image from a build section; digest updates follow images pulled from a registry. " +
				"Rebuild it with a stack build or deploy."}
	}
	ref, err := imageref.Parse(s.Reference)
	pinned := ref.Pinned() || strings.Contains(s.Reference, "@")
	if err != nil && !pinned {
		return Result{Reason: domain.UpdateReasonInvalidRef, Message: "The image reference is not valid: " + err.Error()}
	}
	if pinned {
		return Result{Ref: ref, Reason: domain.UpdateReasonDigestPinned,
			Message: "The image is pinned by digest (@sha256), which never changes. To follow a tag, change your own definition " +
				"to a tag and deploy it."}
	}
	if !imageref.ExplicitTag(s.Reference) {
		return Result{Ref: ref, Reason: domain.UpdateReasonUntagged,
			Message: "The image has no explicit tag (it implies latest). Name a tag in your own definition (for example :1.2 or " +
				":latest) to opt in."}
	}
	switch p := strings.ToLower(strings.TrimSpace(s.PullPolicy)); p {
	case "", "missing", "if_not_present":
	default:
		return Result{Ref: ref, Reason: domain.UpdateReasonPullPolicy, Message: fmt.Sprintf("pull_policy %q conflicts with digest updates: "+
			"never and build keep the image local; always and periodic policies pull on their own at every deploy, bypassing the "+
			"checked candidate and its quarantine. Use pull_policy: missing (the default) in your definition.", s.PullPolicy)}
	}
	r := Result{Eligible: true, Ref: ref, NonVersionTag: !imageref.VersionTag(ref.Tag)}
	if r.NonVersionTag {
		r.Message = fmt.Sprintf("The tag %q is not a version: what it points to can change meaning (for example a new major "+
			"version). Updates follow its digest as it moves.", ref.Tag)
	}
	return r
}
