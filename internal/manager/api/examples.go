package api

import (
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Examples in the OpenAPI document (#4): every JSON request body and every
// 2xx JSON response gets a complete media-type example, and every documented
// error response references a shared example of the error shape for its
// status. Body examples are assembled from the schema: the field examples
// DTOs declare with `example:"..."` tags first, then const, default and
// enum values, then neutral values that satisfy the field's constraints.
// Request examples carry the required fields plus the optional ones whose
// DTO declares an example. TestEveryBodyHasAnExample validates each
// example against its schema and requires every body to declare at least
// one field example.

// maxExampleDepth bounds schema nesting (recursive types stop there).
const maxExampleDepth = 12

// ErrorExampleName is the components.examples key of the error example for
// an HTTP status (e.g. "Error404").
func ErrorExampleName(status int) string { return "Error" + strconv.Itoa(status) }

// addExamples fills in the examples of every registered operation.
func addExamples(a huma.API) {
	oapi := a.OpenAPI()
	reg := oapi.Components.Schemas
	for _, path := range slices.Sorted(maps.Keys(oapi.Paths)) {
		item := oapi.Paths[path]
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Patch, item.Delete} {
			if op == nil {
				continue
			}
			if rb := op.RequestBody; rb != nil {
				if mt := rb.Content["application/json"]; needsExample(mt) {
					if v, ok := (exampleGen{reg: reg, write: true}).value(mt.Schema, "", 0); ok {
						mt.Example = v
					}
				}
			}
			for code, resp := range op.Responses {
				status, err := strconv.Atoi(code)
				if err != nil || resp == nil {
					continue
				}
				switch {
				case status >= 200 && status < 300:
					if mt := resp.Content["application/json"]; needsExample(mt) {
						if v, ok := (exampleGen{reg: reg}).value(mt.Schema, "", 0); ok {
							mt.Example = v
						}
					}
				case status >= 400:
					if mt := resp.Content["application/problem+json"]; mt != nil && mt.Example == nil && len(mt.Examples) == 0 {
						name := ErrorExampleName(status)
						registerErrorExample(oapi, status, name)
						mt.Examples = map[string]*huma.Example{"error": {Ref: "#/components/examples/" + name}}
					}
				}
			}
		}
	}
}

func needsExample(mt *huma.MediaType) bool {
	return mt != nil && mt.Schema != nil && mt.Example == nil && len(mt.Examples) == 0
}

// registerErrorExample adds the example of the error shape for status: the
// first catalog code with that status (the generic one).
func registerErrorExample(oapi *huma.OpenAPI, status int, name string) {
	if oapi.Components.Examples == nil {
		oapi.Components.Examples = map[string]*huma.Example{}
	}
	if _, ok := oapi.Components.Examples[name]; ok {
		return
	}
	code, meaning, retryable := CodeInternal, "", false
	for _, c := range ErrorCodes() {
		if c.Status == status {
			code, meaning, retryable = c.Code, c.Meaning, c.Retryable
			break
		}
	}
	msg := strings.ToLower(http.StatusText(status))
	if msg == "" {
		msg = strings.ReplaceAll(code, "_", " ")
	}
	details := []any{}
	if status == http.StatusUnprocessableEntity {
		details = append(details, map[string]any{"field": "body.name", "message": "expected length >= 1"})
		msg = "invalid input"
	}
	oapi.Components.Examples[name] = &huma.Example{
		Summary:     code,
		Description: meaning,
		Value: map[string]any{
			"code": code, "message": msg, "details": details,
			"requestId": "4f1c2e7a9b0d4c3e8f6a1b2c3d4e5f60", "retryable": retryable,
		},
	}
}

// exampleGen assembles an example value from a schema; write selects the
// request direction (readOnly fields left out, optional fields only when
// they declare an example) over the response direction (all fields but
// writeOnly ones).
type exampleGen struct {
	reg   huma.Registry
	write bool
}

func (g exampleGen) resolve(s *huma.Schema) *huma.Schema {
	if s != nil && s.Ref != "" {
		return g.reg.SchemaFromRef(s.Ref)
	}
	return s
}

// value assembles an example of s; name is the property the value is for
// (placeholders follow it: IDs, names, digests, paths).
func (g exampleGen) value(s *huma.Schema, name string, depth int) (any, bool) {
	if depth > maxExampleDepth {
		return nil, false
	}
	if s = g.resolve(s); s == nil {
		return nil, false
	}
	switch {
	case len(s.Examples) > 0:
		return s.Examples[0], true
	case s.Const != nil:
		return s.Const, true
	case s.Default != nil:
		return s.Default, true
	case len(s.Enum) > 0:
		return s.Enum[0], true
	case len(s.OneOf) > 0:
		return g.value(s.OneOf[0], name, depth+1)
	case len(s.AnyOf) > 0:
		return g.value(s.AnyOf[0], name, depth+1)
	case len(s.AllOf) > 0:
		out := map[string]any{}
		for _, part := range s.AllOf {
			v, ok := g.value(part, name, depth+1)
			m, isMap := v.(map[string]any)
			if !ok || !isMap {
				return nil, false
			}
			maps.Copy(out, m)
		}
		return out, true
	}
	switch s.Type {
	case huma.TypeObject:
		return g.object(s, depth)
	case huma.TypeArray:
		item, ok := g.value(s.Items, strings.TrimSuffix(name, "s"), depth+1)
		n := 1
		if s.MinItems != nil && *s.MinItems > n {
			n = *s.MinItems
		}
		if !ok || (s.MaxItems != nil && *s.MaxItems == 0) {
			return []any{}, true
		}
		out := make([]any, n)
		for i := range out {
			out[i] = item
		}
		return out, true
	case huma.TypeString:
		return exampleString(s, name), true
	case huma.TypeInteger:
		return int64(exampleNumber(s)), true
	case huma.TypeNumber:
		return exampleNumber(s), true
	case huma.TypeBoolean:
		return false, true
	case "":
		return map[string]any{}, true // any JSON value
	}
	return nil, false
}

func (g exampleGen) object(s *huma.Schema, depth int) (any, bool) {
	out := map[string]any{}
	for _, name := range slices.Sorted(maps.Keys(s.Properties)) {
		p := s.Properties[name]
		required := slices.Contains(s.Required, name)
		if rp := g.resolve(p); rp == nil || (g.write && (p.ReadOnly || rp.ReadOnly)) || (!g.write && (p.WriteOnly || rp.WriteOnly)) {
			continue
		}
		if g.write && !required && !g.documented(p, depth+1, map[string]bool{}) {
			continue
		}
		v, ok := g.value(p, name, depth+1)
		if !ok {
			if required {
				return nil, false
			}
			continue
		}
		out[name] = v
	}
	return out, true
}

// documented reports whether the schema (or anything it contains) declares
// an example.
func (g exampleGen) documented(s *huma.Schema, depth int, seen map[string]bool) bool {
	if s == nil || depth > maxExampleDepth {
		return false
	}
	if s.Ref != "" {
		if seen[s.Ref] {
			return false
		}
		seen[s.Ref] = true
		return g.documented(g.reg.SchemaFromRef(s.Ref), depth+1, seen)
	}
	if len(s.Examples) > 0 {
		return true
	}
	for _, p := range s.Properties {
		if g.documented(p, depth+1, seen) {
			return true
		}
	}
	if g.documented(s.Items, depth+1, seen) {
		return true
	}
	if ap, ok := s.AdditionalProperties.(*huma.Schema); ok && g.documented(ap, depth+1, seen) {
		return true
	}
	for _, list := range [][]*huma.Schema{s.OneOf, s.AnyOf, s.AllOf} {
		for _, p := range list {
			if g.documented(p, depth+1, seen) {
				return true
			}
		}
	}
	return false
}

func exampleString(s *huma.Schema, name string) string {
	v := "example"
	lower := strings.ToLower(name)
	switch {
	case name == "id" || strings.HasSuffix(name, "Id"):
		v = "0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f"
	case strings.HasSuffix(lower, "digest"):
		v = "sha256:3f1c2e7a9b0d4c3e8f6a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60"
	case strings.Contains(lower, "environment") && strings.HasSuffix(lower, "name"):
		v = "nas"
	case lower == "name" || strings.HasSuffix(lower, "name"):
		v = "web"
	case lower == "image" || lower == "reference":
		v = "nginx:1.27"
	case lower == "path" || strings.HasSuffix(lower, "path"):
		v = "config/app.conf"
	}
	switch s.Format {
	case "date-time":
		v = "2026-09-25T12:00:00Z"
	case "date":
		v = "2026-09-25"
	case "time":
		v = "12:00:00"
	case "uri", "url":
		v = "https://docker.example.com"
	case "email":
		v = "ada@example.com"
	case "uuid":
		v = "0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f"
	case "ipv4":
		v = "192.0.2.10"
	case "ipv6":
		v = "2001:db8::10"
	case "hostname":
		v = "docker.example.com"
	}
	if s.ContentEncoding == "base64" {
		v = "ZXhhbXBsZQ=="
	}
	if s.MinLength != nil && len(v) < *s.MinLength {
		v += strings.Repeat("x", *s.MinLength-len(v))
	}
	if s.MaxLength != nil && len(v) > *s.MaxLength {
		v = v[:*s.MaxLength]
	}
	return v
}

func exampleNumber(s *huma.Schema) float64 {
	v := 1.0
	if s.Minimum != nil && v < *s.Minimum {
		v = *s.Minimum
	}
	if s.ExclusiveMinimum != nil && v <= *s.ExclusiveMinimum {
		v = *s.ExclusiveMinimum + 1
	}
	if s.Maximum != nil && v > *s.Maximum {
		v = *s.Maximum
	}
	if s.ExclusiveMaximum != nil && v >= *s.ExclusiveMaximum {
		v = *s.ExclusiveMaximum - 1
	}
	return v
}
