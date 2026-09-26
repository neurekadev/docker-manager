package app

import (
	"net/http"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/gitremote/gittest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil/canary"
)

// Image builds (#33) through the real manager: Git credentials (owner
// only, write-only, connection tests against a fake Git server), manual
// builds (202 + job, build records), build definitions and runs, shaping
// and audit without credentials or build argument values.

type gitCredBody struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Status   string `json:"status"`
	View     string `json:"view"`
	Revision int64  `json:"revision"`
	Secret   *struct {
		Set         bool   `json:"set"`
		Fingerprint string `json:"fingerprint"`
		Version     int    `json:"version"`
	} `json:"secret"`
}

type buildBody struct {
	ID              string   `json:"id"`
	JobID           string   `json:"jobId"`
	Status          string   `json:"status"`
	GitURL          string   `json:"gitUrl"`
	GitCredentialID string   `json:"gitCredentialId"`
	BuildArgNames   []string `json:"buildArgNames"`
	DefinitionID    string   `json:"definitionId"`
}

type definitionBody struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	View     string `json:"view"`
	Revision int64  `json:"revision"`
	Source   *struct {
		GitURL    string            `json:"gitUrl"`
		BuildArgs map[string]string `json:"buildArgs"`
	} `json:"source"`
}

func buildEnv(t *testing.T) (*env, *gittest.Server) {
	t.Helper()
	secrets := canary.New()
	git := gittest.New(t, true, "builder", secrets.New(canary.APIToken, "git token"))
	git.Add("/acme/app.git", &gittest.Repo{Head: "refs/heads/main", Private: true, Refs: map[string]string{"refs/heads/main": strings.Repeat("a", 40)}})
	e := newEnv(t, func(o *Options) { o.GitHTTPClient = git.Client() })
	for _, c := range secrets.All() {
		e.secrets.Register(c.Kind, c.Name, c.Value)
	}
	t.Cleanup(func() { scanDatabase(t, e) })
	return e, git
}

func TestImageBuildsThroughTheAPI(t *testing.T) {
	e, git := buildEnv(t)
	owner, _ := e.setupOwner()
	e.seedEnvironment("e1", "NAS")
	host := strings.TrimPrefix(git.Server.URL, "https://")

	// Git credential: 201, token write-only, connection test.
	var gc gitCredBody
	r := owner.must(http.StatusCreated, http.MethodPost, "/api/v1/git-credentials",
		map[string]any{"name": "fake git", "host": host, "username": "builder", "secret": git.Password})
	r.json(t, &gc)
	if gc.Host != host || gc.View != "full" || gc.Secret == nil || !gc.Secret.Set || !strings.HasPrefix(gc.Secret.Fingerprint, "fp_") {
		t.Fatalf("%+v", gc)
	}
	var test struct {
		OK     bool   `json:"ok"`
		Commit string `json:"commit"`
		Head   string `json:"head"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/git-credentials/"+gc.ID+"/connection-tests",
		map[string]any{"repositoryUrl": git.URL("/acme/app.git")}).json(t, &test)
	if !test.OK || test.Commit != strings.Repeat("a", 40) || test.Head != "refs/heads/main" {
		t.Fatalf("%+v", test)
	}
	rotated := e.secrets.New(canary.APIToken, "rotated git token")
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/git-credentials/"+gc.ID, map[string]any{"secret": rotated},
		header("If-Match", r.header.Get("ETag"))).json(t, &gc)
	if gc.Secret.Version != 2 {
		t.Fatalf("%+v", gc)
	}

	// A manual build: 202 + job; the record is the job ID.
	// Build argument values are configuration (job input, definitions),
	// not credentials: checked only against the audit trail.
	args := canary.New()
	argValue := args.New(canary.EnvValue, "build arg value")
	var job struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		State string `json:"state"`
	}
	r = owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/environments/e1/images/builds", map[string]any{
		"gitUrl": git.URL("/acme/app.git"), "ref": "main", "tags": []string{"acme/app:1"}, "buildArgs": map[string]string{"TOKENISH": argValue},
	}, header("Idempotency-Key", "build-1"))
	r.json(t, &job)
	if job.Kind != "image.build" || r.header.Get("Location") != "/api/v1/jobs/"+job.ID {
		t.Fatalf("%+v %v", job, r.header)
	}
	var b buildBody
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/environments/e1/image-builds/"+job.ID, nil).json(t, &b)
	if b.ID != job.ID || b.Status != "queued" || b.GitCredentialID != gc.ID || len(b.BuildArgNames) != 1 || b.BuildArgNames[0] != "TOKENISH" {
		t.Fatalf("%+v", b)
	}
	var page struct {
		Items []buildBody `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/environments/e1/image-builds", nil).json(t, &page)
	if len(page.Items) != 1 {
		t.Fatalf("%+v", page)
	}
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/environments/e1/images/builds",
		map[string]any{"gitUrl": "ssh://git@github.com/acme/app.git", "tags": []string{"acme/app:1"}})
	owner.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/environments/e1/image-builds/missing", nil)

	// Build definitions: CRUD and runs.
	var d definitionBody
	r = owner.must(http.StatusCreated, http.MethodPost, "/api/v1/environments/e1/build-definitions", map[string]any{"name": "app",
		"source": map[string]any{"gitUrl": git.URL("/acme/app.git"), "tags": []string{"acme/app:nightly"}, "buildArgs": map[string]string{"TOKENISH": argValue}}})
	r.json(t, &d)
	if d.View != "full" || d.Source == nil || d.Source.BuildArgs["TOKENISH"] != argValue {
		t.Fatalf("%+v", d)
	}
	owner.fail(http.StatusConflict, "build_definition_name_taken", http.MethodPost, "/api/v1/environments/e1/build-definitions",
		map[string]any{"name": "APP", "source": map[string]any{"gitUrl": git.URL("/acme/app.git"), "tags": []string{"x:1"}}})
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/environments/e1/build-definitions/"+d.ID, map[string]any{"description": "nightly"},
		header("If-Match", r.header.Get("ETag"))).json(t, &d)
	owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/environments/e1/build-definitions/"+d.ID+"/runs", nil).json(t, &job)
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/environments/e1/image-builds/"+job.ID, nil).json(t, &b)
	if b.DefinitionID != d.ID {
		t.Fatalf("%+v", b)
	}
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/environments/e1/build-definitions/"+d.ID, nil,
		header("If-Match", `"`+itoa(int(d.Revision))+`"`))

	// The audit trail names the builds, Git URL and credential IDs, never
	// credentials or build argument values.
	var details []string
	if err := e.m.DB().NewRaw("SELECT details FROM audit_events").Scan(testutil.Context(t), &details); err != nil {
		t.Fatal(err)
	}
	e.secrets.AssertClean(t, "audit details", details)
	args.AssertClean(t, "audit details", details)
	var actions []string
	if err := e.m.DB().NewRaw("SELECT action FROM audit_events ORDER BY seq").Scan(testutil.Context(t), &actions); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"git_credential.create", "git_credential_connection_test.create", "git_credential.update", "image.build",
		"build_definition.manage", "job.queued"} {
		if !containsString(actions, want) {
			t.Errorf("audit lacks %s: %v", want, actions)
		}
	}
}

func TestBuildAccessForOtherUsers(t *testing.T) {
	e, git := buildEnv(t)
	owner, _ := e.setupOwner()
	e.seedEnvironment("e1", "NAS")
	var gc gitCredBody
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/git-credentials", map[string]any{"name": "g",
		"host": strings.TrimPrefix(git.Server.URL, "https://"), "username": "builder", "secret": git.Password}).json(t, &gc)
	body := map[string]any{"gitUrl": git.URL("/acme/app.git"), "tags": []string{"acme/app:1"}}

	rita, ritaID := e.opsUser(owner, "allow api_tokens.create @all")
	rita.fail(http.StatusNotFound, "not_found", http.MethodPost, "/api/v1/environments/e1/images/builds", body)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/git-credentials",
		map[string]any{"name": "x", "host": "github.com", "username": "u", "secret": "whatever-12345"})
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPatch, "/api/v1/git-credentials/"+gc.ID, map[string]any{"name": "y"}, header("If-Match", `"1"`))
	var list struct {
		Items []gitCredBody `json:"items"`
	}
	rita.must(http.StatusOK, http.MethodGet, "/api/v1/git-credentials", nil).json(t, &list)
	if len(list.Items) != 0 {
		t.Fatalf("%+v", list.Items)
	}

	// image.build in e1: builds allowed (the matching credential is used
	// without rita reading it), records need image.read.
	owner.putRules("/api/v1/users/"+ritaID+"/permissions", "allow image.build @env:e1")
	rita.must(http.StatusAccepted, http.MethodPost, "/api/v1/environments/e1/images/builds", body)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/environments/e1/image-builds", nil)
	rita.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/git-credentials/"+gc.ID, nil)

	// A definition-scoped grant runs that definition only.
	var d definitionBody
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/environments/e1/build-definitions",
		map[string]any{"name": "app", "source": body}).json(t, &d)
	owner.putRules("/api/v1/users/"+ritaID+"/permissions", "allow image.build @build_definition:"+d.ID)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/environments/e1/images/builds", body)
	var got definitionBody
	rita.must(http.StatusOK, http.MethodGet, "/api/v1/environments/e1/build-definitions/"+d.ID, nil).json(t, &got)
	if got.View != "minimal" || got.Source != nil {
		t.Fatalf("%+v", got)
	}
	rita.must(http.StatusAccepted, http.MethodPost, "/api/v1/environments/e1/build-definitions/"+d.ID+"/runs", nil)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPatch, "/api/v1/environments/e1/build-definitions/"+d.ID,
		map[string]any{"name": "z"}, header("If-Match", `"1"`))
}
