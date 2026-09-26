// Command routeinventory reconciles api/route-inventory.yaml with
// api/openapi.json and prints the per-issue implementation status as a
// Markdown table (used for the #4 evidence comment and CI summaries).
//
//	go run ./tools/routeinventory
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/api/inventory"
)

func main() {
	invPath := flag.String("inventory", "api/route-inventory.yaml", "route inventory")
	specPath := flag.String("spec", "api/openapi.json", "OpenAPI document")
	flag.Parse()
	if err := run(os.Stdout, *invPath, *specPath); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "routeinventory:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, invPath, specPath string) error {
	inv, err := inventory.Load(invPath)
	if err != nil {
		return err
	}
	spec, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	rep := inventory.Reconcile(inv, spec, nil)
	_, _ = fmt.Fprintf(w, "Route inventory: %d routes, %d implemented, %d planned.\n\n%s",
		len(inv.Routes), rep.Implemented, rep.Planned, inventory.Summary(inv))
	for _, e := range rep.Errors {
		_, _ = fmt.Fprintln(os.Stderr, "error:", e)
	}
	if len(rep.Errors) > 0 {
		return fmt.Errorf("%d reconciliation error(s)", len(rep.Errors))
	}
	return nil
}
