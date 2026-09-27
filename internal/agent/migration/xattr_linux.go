//go:build linux

package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CopyXattrs copies the extended attributes of every directory, regular
// file and FIFO below srcDir to the same entry below dstDir (a copy made
// by CopyTree): POSIX ACLs (system.posix_acl_*), file capabilities
// (security.capability) and user/trusted attributes. SELinux labels are
// not copied: the destination keeps the context of its own location.
// Symlinks are never followed and keep no attributes. Attributes the
// destination refuses are listed in the stats; reading the source failing
// is an error.
func CopyXattrs(ctx context.Context, srcDir, dstDir string) (XattrStats, error) {
	var st XattrStats
	err := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if t := d.Type(); t != 0 && t != fs.ModeDir && t != fs.ModeNamedPipe {
			return nil // symlinks and skipped special files
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if fi, err := os.Lstat(target); err != nil || fi.Mode().Type() != d.Type() {
			return fmt.Errorf("%s: missing from the copy", filepath.ToSlash(rel))
		}
		names, err := listXattrs(p)
		if err != nil {
			return fmt.Errorf("%s: list extended attributes: %w", filepath.ToSlash(rel), err)
		}
		for _, n := range names {
			if n == "security.selinux" {
				continue
			}
			v, err := getXattr(p, n)
			if errors.Is(err, syscall.ENODATA) {
				continue // removed meanwhile
			}
			if err != nil {
				return fmt.Errorf("%s: read extended attribute %s: %w", filepath.ToSlash(rel), n, err)
			}
			if err := syscall.Setxattr(target, n, v, 0); err != nil {
				if len(st.Failed) < MaxXattrFailures {
					st.Failed = append(st.Failed, fmt.Sprintf("%s: %s: %v", filepath.ToSlash(rel), n, err))
				}
				continue
			}
			st.Copied++
		}
		return nil
	})
	return st, err
}

func listXattrs(p string) ([]string, error) {
	for range 3 {
		n, err := syscall.Listxattr(p, nil)
		if errors.Is(err, syscall.ENOTSUP) {
			return nil, nil
		}
		if err != nil || n == 0 {
			return nil, err
		}
		buf := make([]byte, n)
		n, err = syscall.Listxattr(p, buf)
		if errors.Is(err, syscall.ERANGE) {
			continue // grew meanwhile
		}
		if err != nil {
			return nil, err
		}
		var out []string
		for _, s := range strings.Split(string(buf[:n]), "\x00") {
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	}
	return nil, syscall.ERANGE
}

func getXattr(p, name string) ([]byte, error) {
	for range 3 {
		n, err := syscall.Getxattr(p, name, nil)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, n)
		if n == 0 {
			return buf, nil
		}
		n, err = syscall.Getxattr(p, name, buf)
		if errors.Is(err, syscall.ERANGE) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
	return nil, syscall.ERANGE
}
