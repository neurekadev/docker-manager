package restic

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// secretFiles delivers secrets to one restic process.
//
// On Linux each secret is written into a pipe whose read end the child
// inherits (exec.Cmd.ExtraFiles, descriptors 3, 4, ...) and reads through
// /proc/self/fd/N: the secret never touches a disk, an argument or the
// environment, and nothing is left behind if the manager or agent dies.
// Elsewhere (developer machines) each secret is a 0600 file in a private
// 0700 directory removed after the process ends.
type secretFiles struct {
	usePipes bool
	dir      string
	tmpRoot  string
	extra    []*os.File
}

func newSecretFiles(tmpRoot string) (*secretFiles, error) {
	return &secretFiles{usePipes: runtime.GOOS == "linux", tmpRoot: tmpRoot}, nil
}

// add delivers one secret and returns the path the child reads it from.
func (s *secretFiles) add(secret string) (string, error) {
	if s.usePipes {
		r, w, err := os.Pipe()
		if err != nil {
			return "", err
		}
		// A pipe buffers at least 4 KiB (64 KiB on Linux): the write
		// completes before the child starts reading.
		if len(secret) > 4096 {
			_ = r.Close()
			_ = w.Close()
			return "", errors.New("secret too long")
		}
		_, werr := w.WriteString(secret)
		cerr := w.Close()
		if err := errors.Join(werr, cerr); err != nil {
			_ = r.Close()
			return "", err
		}
		s.extra = append(s.extra, r)
		return fdPath(2 + len(s.extra)), nil
	}
	if s.dir == "" {
		if err := os.MkdirAll(s.tmpRoot, 0o700); err != nil {
			return "", err
		}
		d, err := os.MkdirTemp(s.tmpRoot, "restic-secret-")
		if err != nil {
			return "", err
		}
		s.dir = d
	}
	p := filepath.Join(s.dir, "s"+strconv.Itoa(len(s.extra)+countFiles(s.dir)))
	if err := os.WriteFile(p, []byte(secret), 0o600); err != nil {
		return "", err
	}
	return p, nil
}

func countFiles(dir string) int {
	es, _ := os.ReadDir(dir)
	return len(es)
}

// started closes the parent's copies of the child's pipe ends.
func (s *secretFiles) started() {
	for _, f := range s.extra {
		_ = f.Close()
	}
	s.extra = nil
}

// cleanup releases everything (also when the process never started).
func (s *secretFiles) cleanup() {
	s.started()
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}
