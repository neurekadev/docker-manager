package jobspec

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// File jobs act inside one root: a stack's project directory or a volume
// (agent kinds), or a template's draft (manager kinds). Their capabilities
// are per root type (stack.files.copy vs volume.files.copy, #17), so a
// grant on stacks never opens volumes.
var fileRoots = []domain.TargetType{domain.TargetStack, domain.TargetVolume}

// roots returns the root types of a RootScoped kind.
func (s Spec) roots() []domain.TargetType {
	if len(s.Roots) > 0 {
		return s.Roots
	}
	return fileRoots
}

// Capabilities returns the #17 capability keys a manual or API-token
// request of this kind needs on every target, for the given targets and
// canonical input. The job engine checks them when the job is requested and
// again when a queued job is dispatched; job.read/job.cancel also accept
// them (the initiator of a restart may follow it).
//
//   - Plain kinds need Capability.
//   - RootScoped kinds prefix Capability with the type of their single
//     stack or volume target (files.copy -> stack.files.copy).
//   - Kinds with CapabilityByInput need the capability of every top-level
//     input key present (files.metadata: "chmod" -> files.chmod, "chown"
//     -> files.chown); at least one key is required.
func (s Spec) Capabilities(targets []domain.JobTarget, input []byte) ([]string, error) {
	base := []string{s.Capability}
	if len(s.CapabilityByInput) > 0 {
		var fields map[string]json.RawMessage
		if len(input) > 0 {
			if err := json.Unmarshal(input, &fields); err != nil {
				return nil, fmt.Errorf("%w: %s input must be a JSON object", domain.ErrJobInvalid, s.Kind)
			}
		}
		base = nil
		for _, k := range sortedKeys(s.CapabilityByInput) {
			if v, ok := fields[k]; ok && string(v) != "null" {
				base = append(base, s.CapabilityByInput[k])
			}
		}
		if len(base) == 0 {
			return nil, fmt.Errorf("%w: %s input needs at least one of %s", domain.ErrJobInvalid, s.Kind,
				strings.Join(sortedKeys(s.CapabilityByInput), ", "))
		}
	}
	if !s.RootScoped {
		return base, nil
	}
	root := domain.TargetType("")
	for _, t := range targets {
		if !slices.Contains(s.roots(), t.Type) {
			continue
		}
		if root != "" {
			return nil, fmt.Errorf("%w: %s acts inside exactly one stack or volume", domain.ErrJobInvalid, s.Kind)
		}
		root = t.Type
	}
	if root == "" {
		return nil, fmt.Errorf("%w: %s needs a stack or volume target (the file root)", domain.ErrJobInvalid, s.Kind)
	}
	out := make([]string, len(base))
	for i, c := range base {
		out[i] = string(root) + "." + c
	}
	return out, nil
}

// PossibleCapabilities lists every capability key a job of this kind can
// require (catalog completeness checks, #17/#29).
func (s Spec) PossibleCapabilities() []string {
	base := []string{s.Capability}
	if len(s.CapabilityByInput) > 0 {
		base = nil
		for _, k := range sortedKeys(s.CapabilityByInput) {
			base = append(base, s.CapabilityByInput[k])
		}
	}
	if !s.RootScoped {
		return base
	}
	var out []string
	for _, r := range s.roots() {
		for _, c := range base {
			out = append(out, string(r)+"."+c)
		}
	}
	return out
}

// capabilityDoc renders the capability column of the lock matrix.
func (s Spec) capabilityDoc() string {
	c := s.Capability
	if len(s.CapabilityByInput) > 0 {
		var parts []string
		for _, k := range sortedKeys(s.CapabilityByInput) {
			parts = append(parts, s.CapabilityByInput[k])
		}
		c = strings.Join(parts, " / ")
	}
	if s.RootScoped {
		var names []string
		for _, r := range s.roots() {
			names = append(names, string(r))
		}
		prefix := names[0]
		if len(names) > 1 {
			prefix = "{" + strings.Join(names, ",") + "}"
		}
		return "`" + prefix + "." + strings.ReplaceAll(c, " / ", "`, `"+prefix+".") + "`"
	}
	return "`" + c + "`"
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
