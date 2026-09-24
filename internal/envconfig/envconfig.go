// Package envconfig reads DockYard configuration from environment variables.
//
// Secret values support a `<NAME>_FILE` variant that names a file holding the
// value (Docker/Compose secrets). Setting both NAME and NAME_FILE is an error.
// The source is injectable so configuration parsing is unit-testable.
package envconfig

import (
	"fmt"
	"os"
	"strings"
)

// Source provides environment lookups and file reads.
type Source struct {
	LookupEnv func(string) (string, bool)
	ReadFile  func(string) ([]byte, error)
}

// OS returns a Source backed by the process environment and filesystem.
func OS() Source {
	return Source{LookupEnv: os.LookupEnv, ReadFile: os.ReadFile}
}

// Map returns a Source backed by vars; files maps paths to contents.
func Map(vars map[string]string, files map[string]string) Source {
	return Source{
		LookupEnv: func(k string) (string, bool) {
			v, ok := vars[k]
			return v, ok
		},
		ReadFile: func(p string) ([]byte, error) {
			v, ok := files[p]
			if !ok {
				return nil, fmt.Errorf("open %s: %w", p, os.ErrNotExist)
			}
			return []byte(v), nil
		},
	}
}

// String returns the trimmed value of name, or def when unset or empty.
func (s Source) String(name, def string) string {
	if v, ok := s.LookupEnv(name); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return def
}

// Secret returns the value of name or the contents of the file named by
// name_FILE (trailing newlines trimmed). It returns "" when neither is set.
func (s Source) Secret(name string) (string, error) {
	direct, hasDirect := s.LookupEnv(name)
	file, hasFile := s.LookupEnv(name + "_FILE")
	hasDirect = hasDirect && direct != ""
	hasFile = hasFile && strings.TrimSpace(file) != ""
	switch {
	case hasDirect && hasFile:
		return "", fmt.Errorf("%s and %s_FILE are mutually exclusive", name, name)
	case hasFile:
		b, err := s.ReadFile(strings.TrimSpace(file))
		if err != nil {
			// Do not include file contents; the path is operator-provided config.
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	case hasDirect:
		return direct, nil
	default:
		return "", nil
	}
}

// Bool parses a boolean variable (1/0, true/false, yes/no, on/off).
func (s Source) Bool(name string, def bool) (bool, error) {
	v := strings.ToLower(s.String(name, ""))
	switch v {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return def, fmt.Errorf("%s: invalid boolean %q", name, v)
	}
}
