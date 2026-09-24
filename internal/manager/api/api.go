// Package api defines DockYard's public /api/v1 contract with Huma: the
// shared error shape, the operation registration helper with mandatory
// capability/scope metadata, pagination types and the operations themselves.
//
// Types in this package are transport DTOs. Convert to and from domain types
// (internal/domain) in handlers; never expose database models or Docker SDK
// types directly.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/neurekadev/dockyard/internal/buildinfo"
)

// Contract constants.
const (
	// BasePath prefixes every public API route.
	BasePath = "/api/v1"
	// Version is the public API major version (also the OpenAPI info.version).
	Version = "v1"
	// OpenAPIPath serves the spec at OpenAPIPath + ".json" and ".yaml".
	OpenAPIPath = BasePath + "/openapi"
	title       = "DockYard API"
	description = "Public control API of the DockYard manager. All routes live under /api/v1 on the single public origin. " +
		"Errors use the Error schema with media type application/problem+json. " +
		"Every operation declares the capability it requires (x-dockyard-capability) and the scope at which it is checked (x-dockyard-scope)."
)

// Deps are the collaborators operations need. Zero values are valid for spec
// generation (handlers are never called then).
type Deps struct {
	Build buildinfo.Info
	// Readiness reports readiness checks; nil means always ready.
	Readiness func(ctx context.Context) []Check
	// Features are stable feature-flag keys advertised by /capabilities.
	Features []string
}

// Check is one readiness check result.
type Check struct {
	Name    string
	OK      bool
	Message string
}

// Config returns the Huma configuration for the DockYard API.
func Config() huma.Config {
	cfg := huma.DefaultConfig(title, Version)
	cfg.Info.Description = description
	cfg.OpenAPIPath = OpenAPIPath
	cfg.DocsPath = ""    // interactive docs load third-party scripts; not served.
	cfg.SchemasPath = "" // no $schema links in responses.
	cfg.CreateHooks = nil
	cfg.Transformers = []huma.Transformer{errorTransformer}
	return cfg
}

// New creates the Huma API on mux and registers every operation.
func New(mux *http.ServeMux, deps Deps) huma.API {
	a := humago.New(mux, Config())
	registerSystem(a, deps)
	return a
}

// SpecJSON returns the OpenAPI 3.1 document as indented JSON with a trailing
// newline. It is the source of api/openapi.json.
func SpecJSON() ([]byte, error) {
	a := New(http.NewServeMux(), Deps{})
	raw, err := json.Marshal(a.OpenAPI())
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil { // Encode appends the trailing newline
		return nil, err
	}
	return buf.Bytes(), nil
}

// SpecYAML returns the OpenAPI 3.1 document as YAML.
func SpecYAML() ([]byte, error) {
	return New(http.NewServeMux(), Deps{}).OpenAPI().YAML()
}
