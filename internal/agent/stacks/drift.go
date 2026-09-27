package stacks

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
)

// Settings drift (#7 import by copy): the import recreates the containers
// from the project's files, so everything the running containers were
// created with must come from those files. A tool that deployed the
// project may have supplied values from elsewhere (variables from its own
// database or process environment, a changed file after the last deploy):
// recreating would then silently change the service. prepare compares
// every container with what Compose would create from the files and
// refuses on any difference that is not the move itself. Only names of
// fields and keys are reported, never values (environment values are
// secrets).

// classImportDrift: the running containers differ from the files.
const classImportDrift = "import_config_drift"

const recoveryImportDrift = "Nothing changed. The running containers were created with settings that are not in the project's " +
	"files (for example variables the tool that deployed it keeps in its own database, or files edited after its last deploy). " +
	"Put the missing values into the project's files (usually its .env), or redeploy it from that tool so the files match, " +
	"then import it again."

// maxDrift bounds the differences a refusal lists.
const maxDrift = 12

// driftOf lists how the project's containers differ from what Compose
// would create from p, loaded from src (the agent's path of the host
// directory host). Containers of services the files do not define are
// skipped (they are never recreated).
func driftOf(ctx context.Context, eng engine.Engine, p *compose.Project, src, host string, containers []engine.Container) ([]string, error) {
	inspector, _ := eng.(engine.ConfigInspector)
	var out []string
	add := func(svc, format string, args ...any) {
		d := svc + ": " + fmt.Sprintf(format, args...)
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	srcSlash := filepath.ToSlash(src)
	for _, c := range containers {
		svc := c.Labels[lifecycle.ComposeServiceLabel]
		exp, ok := p.Expected(svc)
		if !ok {
			continue
		}
		d, err := eng.InspectContainer(ctx, c.ID)
		if engine.IsCode(err, engine.CodeNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !sameImage(exp.Image, d.Image) {
			add(svc, "image")
		}
		for _, k := range sortedKeys(exp.Labels) {
			if v, ok := d.Labels[k]; !ok || v != exp.Labels[k] {
				add(svc, "label %s", k)
			}
		}
		if exp.Command != nil && !slices.Equal(exp.Command, d.Cmd) {
			add(svc, "command")
		}
		if exp.Entrypoint != nil && !slices.Equal(exp.Entrypoint, d.Entrypoint) {
			add(svc, "entrypoint")
		}
		if exp.User != "" && exp.User != d.User {
			add(svc, "user")
		}
		if exp.WorkingDir != "" && exp.WorkingDir != d.WorkingDir {
			add(svc, "working directory")
		}
		for _, target := range sortedKeys(exp.Mounts) {
			m := exp.Mounts[target]
			want := m.Source
			if m.Type == "bind" {
				want = path.Clean(filepath.ToSlash(want))
				if within(srcSlash, want) {
					want = path.Join(host, strings.TrimPrefix(strings.TrimPrefix(want, srcSlash), "/"))
				}
			}
			i := slices.IndexFunc(d.Mounts, func(cm engine.Mount) bool { return path.Clean(cm.Destination) == path.Clean(target) })
			switch {
			case i < 0:
				add(svc, "mount at %s", target)
			case m.Type == "bind" && (d.Mounts[i].Type != "bind" || path.Clean(filepath.ToSlash(d.Mounts[i].Source)) != want):
				add(svc, "mount at %s", target)
			case m.Type == "volume" && (d.Mounts[i].Type != "volume" || d.Mounts[i].Name != want):
				add(svc, "mount at %s", target)
			}
		}
		if inspector == nil {
			continue
		}
		cc, err := inspector.CreatedConfig(ctx, c.ID)
		if engine.IsCode(err, engine.CodeNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		have, image := envMap(cc.Env), envMap(cc.ImageEnv)
		for _, k := range sortedKeys(exp.Env) {
			if v, ok := have[k]; !ok || v != exp.Env[k] {
				add(svc, "environment variable %s", k)
			}
		}
		for _, k := range sortedKeys(have) {
			if _, ok := exp.Env[k]; ok || k == "PATH" {
				continue
			}
			if v, ok := image[k]; ok && v == have[k] {
				continue
			}
			add(svc, "environment variable %s (not in the files)", k)
		}
		for _, ep := range exp.Ports {
			if !slices.ContainsFunc(cc.Ports, func(cp engine.Port) bool {
				return cp.PrivatePort == ep.Target && cp.PublicPort == ep.Published && strings.EqualFold(cp.Protocol, ep.Protocol) &&
					(ep.HostIP == "" || cp.HostIP == ep.HostIP)
			}) {
				add(svc, "published port %d/%s", ep.Target, ep.Protocol)
			}
		}
		if !exp.Partial {
			for _, cp := range cc.Ports {
				if cp.PublicPort != 0 && !slices.ContainsFunc(exp.Ports, func(ep compose.ExpectedPort) bool {
					return ep.Target == cp.PrivatePort && ep.Published == cp.PublicPort && strings.EqualFold(cp.Protocol, ep.Protocol)
				}) {
					add(svc, "published port %d/%s (not in the files)", cp.PrivatePort, strings.ToLower(cp.Protocol))
				}
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// sameImage compares image references in their short and fully qualified
// Docker Hub forms.
func sameImage(a, b string) bool {
	return a == b || normalizeImage(a) == normalizeImage(b)
}

func normalizeImage(ref string) string {
	name, digest, _ := strings.Cut(ref, "@")
	tag := "latest"
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name, tag = name[:colon], name[colon+1:]
	}
	out := normalizeRepo(name) + ":" + tag
	if digest != "" {
		out += "@" + digest
	}
	return out
}

// driftRefusal refuses an import whose containers differ from the files.
func driftRefusal(diffs []string) error {
	shown := diffs
	more := ""
	if len(shown) > maxDrift {
		shown, more = shown[:maxDrift], fmt.Sprintf(" and %d more", len(diffs)-maxDrift)
	}
	return &stepError{class: classImportDrift, recovery: recoveryImportDrift,
		err: fmt.Errorf("the running containers differ from what the project's files create: %s%s", strings.Join(shown, "; "), more)}
}
