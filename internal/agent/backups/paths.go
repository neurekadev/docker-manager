package backups

import (
	"path/filepath"
	"strings"
)

// snapPath maps an OS path to the form restic stores it in snapshots
// (slash separated; on Windows the drive letter becomes the first element,
// like restic does). On Linux it is the path itself.
func snapPath(p string) string {
	p = filepath.Clean(p)
	if v := filepath.VolumeName(p); v != "" {
		return "/" + strings.TrimSuffix(v, ":") + filepath.ToSlash(strings.TrimPrefix(p, v))
	}
	return filepath.ToSlash(p)
}

// snapPaths maps a list with snapPath.
func snapPaths(ps []string) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, snapPath(p))
	}
	return out
}

// stagedPath is where restic restores snapshot path sp below target.
func stagedPath(target, sp string) string {
	return filepath.Join(target, filepath.FromSlash(strings.TrimPrefix(sp, "/")))
}

// snapWithin reports whether snapshot path p equals or lies below dir.
func snapWithin(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// snapRel returns p relative to dir (both snapshot paths, p within dir).
func snapRel(p, dir string) string {
	if p == dir {
		return "."
	}
	return strings.TrimPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}
