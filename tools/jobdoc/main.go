// Command jobdoc regenerates the lock-matrix table in
// docs/architecture/job-engine.md from the job kind catalog
// (internal/jobspec). scripts/generate.sh runs it.
//
//	go run ./tools/jobdoc          rewrite the table in place
//	go run ./tools/jobdoc -check   exit 1 when the table is stale
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
)

func main() {
	doc := flag.String("doc", "docs/architecture/job-engine.md", "document containing the lock-matrix markers")
	check := flag.Bool("check", false, "fail when the document is stale instead of rewriting it")
	flag.Parse()
	if err := run(*doc, *check); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "jobdoc:", err)
		os.Exit(1)
	}
}

func run(path string, check bool) error {
	cur, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cur = bytes.ReplaceAll(cur, []byte("\r\n"), []byte("\n"))
	next, err := jobspec.ReplaceMatrix(cur)
	if err != nil {
		return err
	}
	if bytes.Equal(cur, next) {
		return nil
	}
	if check {
		return fmt.Errorf("%s: lock matrix is stale; run: bash scripts/generate.sh", path)
	}
	return os.WriteFile(path, next, 0o644) //nolint:gosec // documentation file
}
