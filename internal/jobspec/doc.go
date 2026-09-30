package jobspec

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Markers delimiting the generated lock-matrix table in
// docs/internal/architecture/job-engine.md.
const (
	MatrixBegin = "<!-- BEGIN GENERATED: lock-matrix (scripts/generate.sh; do not edit) -->"
	MatrixEnd   = "<!-- END GENERATED: lock-matrix -->"
)

// MatrixMarkdown renders the lock matrix (one row per kind) as a Markdown
// table. It is the source of the table in docs/internal/architecture/job-engine.md.
func MatrixMarkdown() string {
	var b strings.Builder
	b.WriteString("| Kind | Executor | Capability | Locks | Steps | Offline deadline | Cap class | Compensations | Manager restart |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, s := range Catalog() {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			s.Kind, s.Executor, s.capabilityDoc(), renderLocks(s.Locks), renderSteps(s.Steps),
			orDash(fmtDuration(s.OfflineDeadline)), orDash(s.ConcurrencyClass),
			renderCompensations(s.Compensations), orDash(string(s.OnManagerRestart)))
	}
	return b.String()
}

func renderLocks(rules []LockRule) string {
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		mode := "S"
		if r.Mode == domain.LockExclusive {
			mode = "**X**"
		}
		var src string
		switch r.Source {
		case FromEnvironments:
			src = "each environment"
		case AllInEnvironments:
			src = "all (`*`)"
		case FromTargets:
			src = string(r.TargetType) + " targets"
			if r.Optional {
				src += ", optional"
			}
		}
		parts = append(parts, fmt.Sprintf("`%s` %s (%s)", r.Scope, mode, src))
	}
	return strings.Join(parts, "<br>")
}

func renderSteps(steps []Step) string {
	parts := make([]string, 0, len(steps))
	for _, st := range steps {
		var flags []string
		if st.Idempotent {
			flags = append(flags, "i")
		}
		if st.SafePoint {
			flags = append(flags, "c")
		}
		p := "`" + st.Name + "`"
		if len(flags) > 0 {
			p += " (" + strings.Join(flags, ",") + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " → ")
}

func renderCompensations(cs []Compensation) string {
	if len(cs) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, "`"+c.Name+"`")
	}
	return strings.Join(parts, ", ")
}

func fmtDuration(d time.Duration) string {
	switch {
	case d == 0:
		return ""
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ReplaceMatrix returns doc with the text between the matrix markers
// replaced by the current MatrixMarkdown.
func ReplaceMatrix(doc []byte) ([]byte, error) {
	begin := bytes.Index(doc, []byte(MatrixBegin))
	end := bytes.Index(doc, []byte(MatrixEnd))
	if begin < 0 || end < 0 || end < begin {
		return nil, errors.New("jobspec: lock-matrix markers not found in document")
	}
	var out bytes.Buffer
	out.Write(doc[:begin+len(MatrixBegin)])
	out.WriteString("\n\n")
	out.WriteString(MatrixMarkdown())
	out.WriteString("\n")
	out.Write(doc[end:])
	return out.Bytes(), nil
}
