package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

type seeder struct {
	m       *app.Manager
	base    string
	dataDir string
	log     *slog.Logger
	agents  []*devAgent
	hosts   []*homelabHost
	envs    map[string]string // host name -> environment ID
	jobs    []string
}

func (s *seeder) stopAgents() {
	for _, a := range s.agents {
		a.stop()
	}
}

func (s *seeder) seed(ctx context.Context, accounts bool) error {
	s.envs = map[string]string{}
	s.hosts = newHomelab()
	for _, h := range s.hosts {
		a, err := connectAgent(ctx, s.m, s.base, filepath.Join(s.dataDir, "agents", h.name), h, s.log)
		if err != nil {
			return fmt.Errorf("agent %s: %w", h.name, err)
		}
		s.agents = append(s.agents, a)
		s.envs[h.name] = a.env
		if err := s.waitOnline(ctx, a.env, true); err != nil {
			return fmt.Errorf("agent %s: %w", h.name, err)
		}
		if h.serviceAddress != "" {
			env, err := s.m.Agents().GetEnvironment(ctx, a.env)
			if err != nil {
				return err
			}
			addr := h.serviceAddress
			if _, err := s.m.Agents().UpdateEnvironment(ctx, a.env, env.Revision, domain.EnvironmentPatch{ServiceAddress: &addr}); err != nil {
				return err
			}
		}
		for _, st := range h.stacks {
			if err := s.seedStack(ctx, a.env, st); err != nil {
				return fmt.Errorf("stack %s: %w", st.name, err)
			}
		}
	}
	if accounts {
		if err := s.seedAccountsAndJobs(ctx); err != nil {
			return err
		}
	}
	// The edge agent disconnects: an offline environment with last-known data.
	for i, h := range s.hosts {
		if h.offline {
			s.agents[i].stop()
			if err := s.waitOnline(ctx, s.agents[i].env, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *seeder) waitOnline(ctx context.Context, env string, online bool) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		e, err := s.m.Agents().GetEnvironment(ctx, env)
		if err == nil && e.Online == online {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("environment %s did not become online=%v", env, online)
}

// seedStack records a deployed DockYard stack with display metadata, reads
// its definition from the (simulated) stacks volume as an observed
// revision, and marks that revision applied.
func (s *seeder) seedStack(ctx context.Context, env string, seed stackSeed) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	st := domain.Stack{ID: ids.New(), EnvironmentID: env, Name: seed.name, DisplayName: seed.displayName,
		Meta: domain.DisplayMeta{Description: seed.description, Icon: seed.icon}, ServiceMeta: map[string]domain.DisplayMeta{},
		Root: domain.StackRootStacks, Dir: seed.name, Origin: domain.StackOriginCreated, Status: domain.StackDeployed,
		EngineState: domain.EngineStateRunning, EngineObservedAt: &now, Revision: 1, CreatedAt: now.Add(-21 * 24 * time.Hour), UpdatedAt: now}
	for _, svc := range seed.services {
		st.Services = append(st.Services, domain.StackServiceDef{Name: svc.name, Image: svc.image})
		st.ServiceMeta[svc.name] = domain.DisplayMeta{Description: svc.description, Icon: svc.icon}
		running := 0
		if svc.running {
			running = 1
		}
		st.EngineServices = append(st.EngineServices, domain.StackServiceState{Service: svc.name, Containers: 1, Running: running})
	}
	if err := store.InsertStack(ctx, s.m.DB(), &st); err != nil {
		return err
	}
	if _, err := s.m.Stacks().RecordObserved(ctx, st.ID, domain.RevisionExternal, authz.Service()); err != nil {
		return err
	}
	cur, err := store.GetStack(ctx, s.m.DB(), st.ID)
	if err != nil {
		return err
	}
	applied := now.Add(-seed.deployedAgo)
	cur.Applied, cur.AppliedAt = cur.Observed, &applied
	return store.UpdateStack(ctx, s.m.DB(), &cur)
}

// apiClient is a browser-like client of the public API (cookie session).
type apiClient struct {
	base string
	http *http.Client
}

func newAPIClient(base string) *apiClient {
	jar, _ := cookiejar.New(nil)
	return &apiClient{base: base, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (c *apiClient) do(ctx context.Context, method, path string, body any, out any, headers ...string) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// seedAccountsAndJobs creates the owner (first-run setup) and a Restricted
// guest (invitation) through the public API, then runs a few jobs as the
// owner: a restart that succeeds, a start that fails and a prune run where
// one removal fails (partial).
func (s *seeder) seedAccountsAndJobs(ctx context.Context) error {
	owner := newAPIClient(s.base)
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/setup/owner", map[string]string{"username": ownerUser,
		"displayName": "Homelab Admin", "password": ownerPassword}, nil); err != nil {
		return err
	}
	var inv struct {
		Code string `json:"code"`
	}
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/invitations", map[string]any{}, &inv, "Idempotency-Key", "devstack-guest"); err != nil {
		return err
	}
	guest := newAPIClient(s.base)
	if _, err := guest.do(ctx, http.MethodPost, "/api/v1/invitations/redemptions", map[string]string{"code": inv.Code, "username": guestUser,
		"displayName": "Guest", "password": guestPassword}, nil); err != nil {
		return err
	}

	homelab, nas := s.hosts[0], s.hosts[1]
	hl, nasEnv := s.envs["homelab"], s.envs["nas"]
	id := func(h *homelabHost, name string) string {
		c, _ := h.engine.Container(name)
		return c.Details.ID
	}
	type jobRef struct {
		ID string `json:"id"`
	}
	var j jobRef
	path := func(env, ctr, action string) string {
		return "/api/v1/environments/" + env + "/containers/" + ctr + "/" + action
	}
	if _, err := owner.do(ctx, http.MethodPost, path(hl, id(homelab, "homeassistant"), "restart"), map[string]any{}, &j, "Idempotency-Key", "devstack-restart"); err != nil {
		return err
	}
	s.jobs = append(s.jobs, j.ID)
	if err := s.waitJob(ctx, owner, j.ID); err != nil {
		return err
	}
	homelab.engine.Fail("container.start", enginefake.Err("container.start", engine.CodeConflict,
		"driver failed programming external connectivity: bind for 0.0.0.0:8080 failed: port is already allocated"))
	if _, err := owner.do(ctx, http.MethodPost, path(hl, id(homelab, "backup-runner"), "start"), map[string]any{}, &j, "Idempotency-Key", "devstack-start"); err != nil {
		return err
	}
	s.jobs = append(s.jobs, j.ID)
	if err := s.waitJob(ctx, owner, j.ID); err != nil {
		return err
	}

	var pol struct {
		ID string `json:"id"`
	}
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/maintenance-policies", map[string]any{"environmentId": nasEnv, "name": "Stopped containers",
		"rules": []map[string]any{{"category": "stopped_containers", "enabled": true, "minAgeHours": 1}}}, &pol); err != nil {
		return err
	}
	nas.engine.Fail("container.remove", enginefake.Err("container.remove", engine.CodeEngineError,
		"driver \"overlay2\" failed to remove root filesystem: device or resource busy"))
	if _, err := owner.do(ctx, http.MethodPost, "/api/v1/maintenance-policies/"+pol.ID+"/runs", map[string]any{"confirm": true}, &j); err != nil {
		return err
	}
	s.jobs = append(s.jobs, j.ID)
	return s.waitJob(ctx, owner, j.ID)
}

func (s *seeder) waitJob(ctx context.Context, c *apiClient, id string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var j struct {
			State string `json:"state"`
		}
		if _, err := c.do(ctx, http.MethodGet, "/api/v1/jobs/"+id, nil, &j); err != nil {
			return err
		}
		if domain.JobState(j.State).Terminal() {
			s.log.Info("seeded job", "job_id", id, "state", j.State)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("job " + id + " did not finish")
}

func (s *seeder) printSummary(w io.Writer, accounts bool) {
	_, _ = fmt.Fprintf(w, "\nDockYard devstack is running: %s\n\n", s.base)
	for _, h := range s.hosts {
		state := "online"
		if h.offline {
			state = "offline"
		}
		_, _ = fmt.Fprintf(w, "  environment %-8s %s (%s)\n", h.name, s.envs[h.name], state)
	}
	if accounts {
		_, _ = fmt.Fprintf(w, "\n  owner  %s / %s\n  guest  %s / %s (Restricted: no access)\n", ownerUser, ownerPassword, guestUser, guestPassword)
	} else {
		_, _ = fmt.Fprintf(w, "\n  first-run setup is open: %s/setup\n", s.base)
	}
	_, _ = fmt.Fprintf(w, "\nLocal development only (plain HTTP on localhost). Ctrl+C stops it.\n\n")
}
