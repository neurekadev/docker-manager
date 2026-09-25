package jobspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// CredentialRefs names the manager-owned credentials a job may use (#19
// registry connections, #33 Git credentials). Job inputs embed it at the top
// level; the manager resolves the IDs to secrets at every dispatch
// (protocol.CommandSecrets) and never stores a secret in the job:
//
//	{"reference": "ghcr.io/org/app:1", "registryConnections": ["0190..."]}
//
// An API request never submits credentials; it names (or lets the manager
// select) a connection whose ID the handler puts here.
type CredentialRefs struct {
	RegistryConnections []string `json:"registryConnections,omitempty"`
	GitCredentials      []string `json:"gitCredentials,omitempty"`
}

// Empty reports whether no credential is referenced.
func (c CredentialRefs) Empty() bool { return len(c.RegistryConnections)+len(c.GitCredentials) == 0 }

// MaxCredentialRefs bounds the credentials of one job.
const MaxCredentialRefs = 32

// CredentialRefsOf extracts the credential references of a job input
// (other keys are ignored). A null or empty input has none.
func CredentialRefsOf(input json.RawMessage) (CredentialRefs, error) {
	var refs CredentialRefs
	if len(bytes.TrimSpace(input)) == 0 || bytes.Equal(bytes.TrimSpace(input), []byte("null")) {
		return refs, nil
	}
	if err := json.Unmarshal(input, &refs); err != nil {
		return CredentialRefs{}, fmt.Errorf("jobspec: credential references: %w", err)
	}
	if len(refs.RegistryConnections)+len(refs.GitCredentials) > MaxCredentialRefs {
		return CredentialRefs{}, errors.New("jobspec: too many credential references")
	}
	for _, id := range append(append([]string{}, refs.RegistryConnections...), refs.GitCredentials...) {
		if id == "" || len(id) > 64 {
			return CredentialRefs{}, errors.New("jobspec: invalid credential reference")
		}
	}
	return refs, nil
}
