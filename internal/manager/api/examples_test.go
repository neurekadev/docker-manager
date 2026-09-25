package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// TestEveryBodyHasAnExample (#4): every JSON request body and 2xx JSON
// response of the catalog has a media-type example that validates against
// its schema, every documented error response references the error example
// of its status, and every body declares at least one field example of its
// own (so examples show real values, not only placeholders).
func TestEveryBodyHasAnExample(t *testing.T) {
	a := New(http.NewServeMux(), Deps{})
	if n := checkExamples(t, a); n < 300 {
		t.Fatalf("checked %d bodies, want the whole catalog", n)
	}
}

// checkExamples reports every body without a valid example and returns the
// number of bodies checked.
func checkExamples(t reporter, a huma.API) int {
	t.Helper()
	oapi := a.OpenAPI()
	reg := oapi.Components.Schemas
	checked := 0
	check := func(where string, mt *huma.MediaType, mode huma.ValidateMode) {
		checked++
		if mt.Example == nil {
			t.Errorf("%s: no example", where)
			return
		}
		if !(exampleGen{reg: reg}).documented(mt.Schema, 0, map[string]bool{}) {
			t.Errorf("%s: the body declares no field example (add example:\"...\" tags to its DTO)", where)
		}
		raw, err := json.Marshal(mt.Example)
		if err != nil {
			t.Errorf("%s: example does not marshal: %v", where, err)
			return
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		res := &huma.ValidateResult{}
		huma.Validate(reg, mt.Schema, huma.NewPathBuffer([]byte(""), 0), mode, v, res)
		for _, e := range res.Errors {
			t.Errorf("%s: example %s is invalid: %v", where, raw, e)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(oapi.Paths)) {
		item := oapi.Paths[path]
		for method, op := range map[string]*huma.Operation{"GET": item.Get, "PUT": item.Put, "POST": item.Post, "PATCH": item.Patch, "DELETE": item.Delete} {
			if op == nil {
				continue
			}
			where := method + " " + path + " (" + op.OperationID + ")"
			if op.RequestBody != nil {
				for ct, mt := range op.RequestBody.Content {
					if ct == "application/json" {
						check(where+" request", mt, huma.ModeWriteToServer)
					}
				}
			}
			for code, resp := range op.Responses {
				status, err := strconv.Atoi(code)
				if err != nil {
					continue
				}
				for ct, mt := range resp.Content {
					switch {
					case status < 300 && ct == "application/json":
						check(where+" "+code+" response", mt, huma.ModeReadFromServer)
					case status >= 400 && ct == "application/problem+json":
						ex := mt.Examples["error"]
						name := ErrorExampleName(status)
						if ex == nil || ex.Ref != "#/components/examples/"+name || oapi.Components.Examples[name] == nil {
							t.Errorf("%s %d: error response without the %s example", where, status, name)
							continue
						}
						if got := oapi.Components.Examples[name].Value.(map[string]any)["code"]; !IsErrorCode(got) {
							t.Errorf("%s: example code %v is not in the catalog", name, got)
						}
					}
				}
			}
		}
	}
	return checked
}

// IsErrorCode reports whether v is a catalogued error code (test helper).
func IsErrorCode(v any) bool {
	s, _ := v.(string)
	for _, c := range ErrorCodes() {
		if c.Code == s {
			return true
		}
	}
	return false
}

type exampleProbe struct {
	Body struct {
		Count int    `json:"count" minimum:"3"`
		Note  string `json:"note,omitempty"`
	}
}

type exampleProbeOut struct {
	Body struct {
		Name string `json:"name" example:"web"`
	}
}

// The generated examples honor constraints and leave undocumented optional
// request fields out; the check catches a body without field examples, an
// example that violates its schema and an error response without its
// example.
func TestExampleCheckCatchesViolations(t *testing.T) {
	a := humago.New(http.NewServeMux(), Config())
	Register(a, Operation{
		Operation: huma.Operation{OperationID: "probe", Method: http.MethodPost, Path: BasePath + "/probe", Summary: "probe",
			Errors: []int{http.StatusNotFound}},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(context.Context, *exampleProbe) (*exampleProbeOut, error) { return nil, nil })
	addExamples(a)
	op := a.OpenAPI().Paths[BasePath+"/probe"].Post
	req := op.RequestBody.Content["application/json"]
	resp := op.Responses["200"].Content["application/json"]
	if b, _ := json.Marshal(req.Example); string(b) != `{"count":3}` {
		t.Fatalf("request example %s", b)
	}
	if b, _ := json.Marshal(resp.Example); string(b) != `{"name":"web"}` {
		t.Fatalf("response example %s", b)
	}
	if ex := op.Responses["404"].Content["application/problem+json"].Examples["error"]; ex == nil || ex.Ref != "#/components/examples/Error404" {
		t.Fatalf("404 example %+v", ex)
	}
	ft := &fakeT{T: t}
	if n := checkExamples(ft, a); n != 2 || ft.errors != 1 { // the request body declares no field example
		t.Fatalf("checked %d bodies, %d problems; want 2, 1", n, ft.errors)
	}
	resp.Example = map[string]any{"name": 5}
	delete(op.Responses["404"].Content["application/problem+json"].Examples, "error")
	ft = &fakeT{T: t}
	if checkExamples(ft, a); ft.errors != 3 {
		t.Fatalf("%d problems, want 3", ft.errors)
	}
}
