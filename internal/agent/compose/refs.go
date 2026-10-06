package compose

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	interp "github.com/compose-spec/compose-go/v2/interpolation"
	"github.com/compose-spec/compose-go/v2/paths"
	"github.com/compose-spec/compose-go/v2/types"
	"go.yaml.in/yaml/v4"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
)

// maxReferenceDepth bounds the include and extends chains the walk follows
// (compose-go reports cycles itself; this only stops a runaway walk).
const maxReferenceDepth = 32

// ReferencedFiles lists the files the Compose files of spec pull in
// through include and extends: file: (absolute paths inside spec.Dir),
// best effort: files outside the project directory and declarations that
// do not parse are left out. It is for a definition that does not load,
// whose files are still read and recorded; LoadProject adds the same files
// to Project.DefinitionFiles.
func ReferencedFiles(spec ProjectSpec) []string {
	configs, err := configFiles(spec)
	if err != nil {
		return nil
	}
	envFiles, err := envFilesOf(spec)
	if err != nil {
		return nil
	}
	w := walkReferences(spec, configs, envFiles)
	return w.files
}

// references walks the include and extends declarations of a project.
// compose-go reports only first-level declarations to its listeners, so
// the walk is Docker Manager's own; it resolves every path the way
// compose-go does:
//
//   - include paths, project_directory and env_file of a top-level Compose
//     file relative to the project directory, an included file's own
//     include paths and extends files relative to its project directory
//     (the first path's directory unless project_directory sets one), with
//     the include's env files (or its project directory's .env) added to
//     the interpolation environment;
//   - extends: file: of a top-level or included Compose file relative to
//     that file's working directory, and an extended file's own extends
//     relative to the extended file's directory, following only the chain
//     of the extended service (compose-go ignores includes there).
//
// Everything such a declaration loads is part of the definition, so it
// must be a file inside the project directory (also through symlinks);
// remote includes and paths compose-go would resolve against the agent's
// own working directory (a relative project_directory or env_file of an
// include inside an included file) are refused. Top-level Compose and env
// files come from spec.Content when it is set, like the load; everything
// else is read from disk, as compose-go reads it.
type references struct {
	dir     string
	realDir string // dir with symlinks resolved; "" when it does not exist
	files   []string
	visited map[string]bool
	// refused is the first declaration that may not be loaded.
	refused error
	// incomplete is set when a file did not parse or interpolate, so the
	// files below it are unknown (the load reports the error).
	incomplete bool
}

func walkReferences(spec ProjectSpec, configs, envFiles []string) *references {
	w := &references{dir: spec.Dir, visited: map[string]bool{}}
	if real, err := filepath.EvalSymlinks(spec.Dir); err == nil {
		w.realDir = real
	}
	env, err := topLevelEnv(spec, envFiles)
	if err != nil {
		w.incomplete = true
	}
	for _, f := range configs {
		var content []byte
		if spec.Content != nil {
			content = spec.Content[w.rel(f)]
			if content == nil {
				content = []byte{}
			}
		}
		w.composeFile(f, content, spec.Dir, env, false, 0)
	}
	slices.Sort(w.files)
	w.files = slices.Compact(w.files)
	return w
}

// topLevelEnv is the interpolation environment of the top-level Compose
// files: the project's env files, never the agent's own environment.
func topLevelEnv(spec ProjectSpec, envFiles []string) (types.Mapping, error) {
	if spec.Content == nil {
		return dotenv.GetEnvFromFile(map[string]string{}, envFiles)
	}
	env := types.Mapping{}
	for _, f := range envFiles {
		r, _ := filepath.Rel(spec.Dir, f)
		vars, err := dotenv.UnmarshalBytesWithLookup(spec.Content[filepath.ToSlash(r)], func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		})
		if err != nil {
			return env, err
		}
		env.Merge(vars)
	}
	return env, nil
}

// composeFile walks a top-level or included Compose file. content is nil
// for a file read from disk. workingDir resolves its include and extends
// paths; nested marks an included file.
func (w *references) composeFile(file string, content []byte, workingDir string, env types.Mapping, nested bool, depth int) {
	key := "compose\x00" + file + "\x00" + workingDir + "\x00" + envKey(env)
	if w.visited[key] {
		return
	}
	w.visited[key] = true
	if depth > maxReferenceDepth {
		w.incomplete = true
		return
	}
	docs, ok := w.parse(file, content, env)
	if !ok {
		return
	}
	for _, doc := range docs {
		if inc, ok := doc["include"]; ok {
			w.includes(file, inc, workingDir, env, nested, depth)
		}
		services, _ := doc["services"].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(services)) {
			svc, _ := services[name].(map[string]any)
			ext, _ := svc["extends"].(map[string]any)
			ref, _ := ext["service"].(string)
			if f, ok := ext["file"].(string); ok && ref != "" {
				w.extends(file, name, f, ref, workingDir, env, depth+1)
			}
		}
	}
}

// includes walks the include section of file.
func (w *references) includes(file string, section any, workingDir string, env types.Mapping, nested bool, depth int) {
	if section == nil {
		return
	}
	entries, ok := section.([]any)
	if !ok {
		w.incomplete = true
		return
	}
	for _, e := range entries {
		var declared, envFiles []string
		var projectDir string
		switch v := e.(type) {
		case string:
			declared = []string{v}
		case map[string]any:
			declared = stringList(v["path"])
			envFiles = stringList(v["env_file"])
			projectDir, _ = v["project_directory"].(string)
		}
		if len(declared) == 0 {
			w.incomplete = true
			continue
		}
		var abs []string
		for _, p := range declared {
			if isRemoteContext(p) {
				w.refuse("%s: remote include %s is not supported: include only files inside the project directory", w.rel(file), p)
				abs = nil
				break
			}
			a := joinPath(workingDir, p)
			if !w.inside(file, "includes", a) {
				abs = nil
				break
			}
			abs = append(abs, a)
		}
		if abs == nil {
			continue
		}
		w.files = append(w.files, abs...)
		if nested && ((projectDir != "" && !filepath.IsAbs(projectDir)) || slices.ContainsFunc(envFiles, func(f string) bool {
			return f != "/dev/null" && !filepath.IsAbs(f)
		})) {
			w.refuse("%s: an include inside an included file cannot set a relative project_directory or env_file "+
				"(it would not resolve against the project directory); use an absolute path inside the project directory", w.rel(file))
			continue
		}
		dir := filepath.Dir(abs[0])
		if projectDir != "" {
			dir = joinPath(workingDir, projectDir)
		}
		var envPaths []string
		if len(envFiles) == 0 {
			if st, err := os.Stat(filepath.Join(dir, ".env")); err == nil && !st.IsDir() {
				envFiles = []string{filepath.Join(dir, ".env")}
			}
		}
		refused := false
		for _, f := range envFiles {
			if f == "/dev/null" {
				continue
			}
			a := joinPath(workingDir, f)
			if !w.inside(file, "includes the env file", a) {
				refused = true
				break
			}
			envPaths = append(envPaths, a)
		}
		if refused {
			continue
		}
		w.files = append(w.files, envPaths...)
		fromFiles, err := dotenv.GetEnvFromFile(env, envPaths)
		if err != nil {
			w.incomplete = true
			continue
		}
		next := env.Clone().Merge(fromFiles)
		for _, a := range abs {
			w.composeFile(a, nil, dir, next, true, depth+1)
		}
	}
}

// extends follows service name's extends: file: in from to service ref
// of the extended file, then that service's own extends chain.
func (w *references) extends(from, name, file, ref, workingDir string, env types.Mapping, depth int) {
	if isRemoteContext(file) {
		w.refuse("%s: service %q extends the remote file %s, which is not supported", w.rel(from), name, file)
		return
	}
	abs := joinPath(workingDir, file)
	if !w.inside(from, fmt.Sprintf("service %q extends", name), abs) {
		return
	}
	w.files = append(w.files, abs)
	w.extended(abs, ref, env, depth)
}

// extended walks service ref of an extended file.
func (w *references) extended(file, ref string, env types.Mapping, depth int) {
	key := "extends\x00" + file + "\x00" + ref + "\x00" + envKey(env)
	if w.visited[key] {
		return
	}
	w.visited[key] = true
	if depth > maxReferenceDepth {
		w.incomplete = true
		return
	}
	docs, ok := w.parse(file, nil, env)
	if !ok {
		return
	}
	for _, doc := range docs {
		services, _ := doc["services"].(map[string]any)
		svc, _ := services[ref].(map[string]any)
		switch ext := svc["extends"].(type) {
		case string:
			w.extended(file, ext, env, depth+1)
		case map[string]any:
			next, _ := ext["service"].(string)
			if next == "" {
				continue
			}
			if f, ok := ext["file"].(string); ok {
				w.extends(file, ref, f, next, filepath.Dir(file), env, depth+1)
			} else {
				w.extended(file, next, env, depth+1)
			}
		}
	}
}

// parse reads a Compose file (content, or the file on disk when content
// is nil) and interpolates its include and extends declarations. A file
// that does not exist or parse ends the walk below it.
func (w *references) parse(file string, content []byte, env types.Mapping) ([]map[string]any, bool) {
	if content == nil {
		b, err := os.ReadFile(file) //nolint:gosec // checked to be inside the project directory
		if err != nil {
			return nil, false // compose-go reports the missing file
		}
		content = b
	}
	var out []map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			w.incomplete = true
			return nil, false
		}
		decl := map[string]any{}
		if inc, ok := doc["include"]; ok {
			decl["include"] = inc
		}
		if services, ok := doc["services"].(map[string]any); ok {
			exts := map[string]any{}
			for name, s := range services {
				if svc, ok := s.(map[string]any); ok && svc["extends"] != nil {
					exts[name] = map[string]any{"extends": svc["extends"]}
				}
			}
			decl["services"] = exts
		}
		decl, err = interp.Interpolate(decl, interp.Options{LookupValue: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		}})
		if err != nil {
			w.incomplete = true
			return nil, false
		}
		out = append(out, decl)
	}
	return out, true
}

// inside reports whether path, which file declares (what: "includes",
// "service \"web\" extends"), lies inside the project directory, also
// through symlinks; otherwise the declaration is refused.
func (w *references) inside(file, what, path string) bool {
	r, err := filepath.Rel(w.dir, path)
	ok := err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
	if ok && w.realDir != "" {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			r, err = filepath.Rel(w.realDir, real)
			ok = err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
		}
	}
	if !ok {
		w.refuse("%s %s %s, which is outside the project directory: a stack's definition may only load files inside it",
			w.rel(file), what, path)
	}
	return ok
}

func (w *references) refuse(format string, args ...any) {
	if w.refused == nil {
		w.refused = engine.Errorf("compose.load", engine.CodeUnsupportedFeature, format, args...)
	}
}

// rel names a file of the project by its project-relative path (the key
// of spec.Content), and a file outside it by its absolute path.
func (w *references) rel(file string) string {
	if r, err := filepath.Rel(w.dir, file); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(r)
	}
	return file
}

// joinPath resolves a declared path against dir. A path starting with ~
// is taken as the home directory compose-go expands it to in extended
// files (so it is refused as outside the project).
func joinPath(dir, p string) string {
	if strings.HasPrefix(p, "~") {
		return paths.ExpandUser(p)
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

// stringList reads a Compose string-or-list value.
func stringList(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// envKey identifies an interpolation environment (an included file may
// load different files under different environments).
func envKey(env types.Mapping) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		fmt.Fprintf(&b, "%d:%s=%d:%s;", len(k), k, len(env[k]), env[k])
	}
	return b.String()
}
