package testharness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitFixturesDir holds one directory per seeded repository, relative to the
// repository root (test/fixtures/git/<name>/...).
const GitFixturesDir = "test/fixtures/git"

// GitDefaultBranch is the branch seeded repositories are committed on.
const GitDefaultBranch = "main"

// SeedBareRepo commits the files of srcDir (one commit on main, fixed author
// and dates so commit IDs are reproducible) and writes a bare repository
// to dst prepared for the dumb HTTP protocol (git update-server-info), so
// any static file server can serve it. It needs the git executable.
func SeedBareRepo(ctx context.Context, srcDir, dst string) error {
	work, err := os.MkdirTemp("", "dockyard-git-seed-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	if err := copyTree(srcDir, work); err != nil {
		return err
	}
	steps := [][]string{
		{"init", "--quiet", "--initial-branch=" + GitDefaultBranch},
		{"add", "--all"},
		{"commit", "--quiet", "--no-gpg-sign", "-m", "seed " + filepath.Base(srcDir)},
	}
	for _, args := range steps {
		if err := runGit(ctx, work, args...); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	// --no-local forces a packed transfer, so the bare repository has a pack
	// plus objects/info/packs for dumb HTTP clients.
	if err := runGit(ctx, "", "clone", "--quiet", "--bare", "--no-local", work, dst); err != nil {
		return err
	}
	return runGit(ctx, dst, "update-server-info")
}

// SeedGitFixtures seeds every directory under test/fixtures/git into
// dst/<name>.git and returns the repository names.
func SeedGitFixtures(ctx context.Context, dst string) ([]string, error) {
	root, err := RepoRoot()
	if err != nil {
		return nil, err
	}
	src := filepath.Join(root, filepath.FromSlash(GitFixturesDir))
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := SeedBareRepo(ctx, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()+".git")); err != nil {
			return nil, fmt.Errorf("seed %s: %w", e.Name(), err)
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no fixture repositories in %s", src)
	}
	return names, nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: fixed executable, arguments from the harness
	cmd.Dir = dir
	// Reproducible commits, independent of the developer's git config.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=DockYard Fixture",
		"GIT_AUTHOR_EMAIL=fixture@dockyard.invalid",
		"GIT_COMMITTER_NAME=DockYard Fixture",
		"GIT_COMMITTER_EMAIL=fixture@dockyard.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) {
			return fmt.Errorf("git executable not found: %w", err)
		}
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

// copyTree copies regular files and directories from src to dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: only regular files are supported in git fixtures", path)
		}
		data, err := os.ReadFile(path) //nolint:gosec // G122: walks trusted repository fixtures
		if err != nil {
			return err
		}
		// Normalize line endings: Windows checkouts may carry CRLF.
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		return os.WriteFile(target, data, 0o644) //nolint:gosec // G703: target is below a fresh temp dir
	})
}
