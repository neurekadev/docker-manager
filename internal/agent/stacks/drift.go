package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
// secrets); mounts name their sources (host paths and volume names). The
// image of a build-only service is not compared: the tool that built it
// chose its name, and the import keeps the image it runs (importRecreate
// tags it with Compose's name).

// classImportDrift: the running containers differ from the files.
const classImportDrift = "import_config_drift"

const recoveryImportDrift = "Nothing changed. The running containers were created with settings that are not in the project's " +
	"files. Usually its files were edited after its last deploy: redeploy it from the tool that manages it so its containers " +
	"match the files. Or that tool supplied values it keeps in its own database: put them into the project's files (usually " +
	"its .env). Then import it again."

// recoveryImportEdited: a definition file is newer than the containers.
const recoveryImportEdited = "Nothing changed. The project's files were changed after its containers were created, so the " +
	"containers still run the previous settings. Redeploy it from the tool that manages it (or undo the edit) so its " +
	"containers match the files, then import it again."

// editedLayout formats the times of an edit after the last deploy.
const editedLayout = "2006-01-02 15:04 UTC"

// maxDrift bounds the differences a refusal lists.
const maxDrift = 12

// driftOf lists how the project's containers differ from what Compose
// would create from p, loaded from src (the agent's path of the host
// directory host), and when the oldest differing container was created.
// Containers of services the files do not define are skipped (they are
// never recreated).
func driftOf(ctx context.Context, eng engine.Engine, p *compose.Project, src, host string, containers []engine.Container) ([]string, time.Time, error) {
	inspector, _ := eng.(engine.ConfigInspector)
	var out []string
	var created, oldest time.Time
	add := func(svc, format string, args ...any) {
		if !created.IsZero() && (oldest.IsZero() || created.Before(oldest)) {
			oldest = created
		}
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
			return nil, time.Time{}, err
		}
		created = d.Created
		if !exp.Built && !sameImage(exp.Image, d.Image) {
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
				add(svc, "mount at %s (in the files, not in the container)", target)
			case m.Type == "bind" && (d.Mounts[i].Type != "bind" || path.Clean(filepath.ToSlash(d.Mounts[i].Source)) != want):
				add(svc, "mount at %s (the files bind %s, the container has %s)", target, want, mountOf(d.Mounts[i]))
			case m.Type == "volume" && (d.Mounts[i].Type != "volume" || d.Mounts[i].Name != want):
				add(svc, "mount at %s (the files mount volume %s, the container has %s)", target, want, mountOf(d.Mounts[i]))
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
			return nil, time.Time{}, err
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
	return out, oldest, nil
}

// mountOf describes a container's mount by its source.
func mountOf(m engine.Mount) string {
	switch m.Type {
	case "bind":
		return "bind " + path.Clean(filepath.ToSlash(m.Source))
	case "volume":
		return "volume " + m.Name
	}
	return m.Type
}

// editedAfter names the newest of the definition files when it changed
// after created (when the oldest differing container was created): the
// files were most likely edited without a redeploy. "" otherwise.
func editedAfter(files []string, src string, created time.Time) string {
	if created.IsZero() {
		return ""
	}
	var name string
	var newest time.Time
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil || !fi.ModTime().After(newest) {
			continue
		}
		name, newest = f, fi.ModTime()
	}
	if name == "" || !newest.After(created) {
		return ""
	}
	rel, err := filepath.Rel(src, name)
	if err != nil {
		rel = filepath.Base(name)
	}
	return fmt.Sprintf("%s was changed on %s, after the containers were created on %s", filepath.ToSlash(rel),
		newest.UTC().Format(editedLayout), created.UTC().Format(editedLayout))
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

// driftRefusal refuses an import whose containers differ from the files;
// edited (editedAfter) tells that the files changed after the deploy.
func driftRefusal(diffs []string, edited string) error {
	shown := diffs
	more := ""
	if len(shown) > maxDrift {
		shown, more = shown[:maxDrift], fmt.Sprintf(" and %d more", len(diffs)-maxDrift)
	}
	msg := fmt.Sprintf("the running containers differ from what the project's files create: %s%s", strings.Join(shown, "; "), more)
	recovery := recoveryImportDrift
	if edited != "" {
		msg += ". " + edited
		recovery = recoveryImportEdited
	}
	return &stepError{class: classImportDrift, recovery: recovery, err: errors.New(msg)}
}
