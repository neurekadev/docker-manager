package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var codeRE = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

func TestErrorCatalogConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range ErrorCodes() {
		if !codeRE.MatchString(c.Code) {
			t.Errorf("code %q is not snake_case", c.Code)
		}
		if seen[c.Code] {
			t.Errorf("code %q listed twice", c.Code)
		}
		seen[c.Code] = true
		if http.StatusText(c.Status) == "" || c.Status < 400 {
			t.Errorf("code %q has status %d", c.Code, c.Status)
		}
		if c.Meaning == "" || c.Owner < 1 || c.Owner > 35 {
			t.Errorf("code %q lacks meaning or owner issue", c.Code)
		}
		if c.Retryable != defaultRetryable(c.Status) && c.Code != CodeIdempotencyKeyInFlight {
			t.Errorf("code %q: retryable %v differs from the status default; document the exception", c.Code, c.Retryable)
		}
	}
	// Every default status code maps to a cataloged code.
	for status := 400; status < 600; status++ {
		if http.StatusText(status) == "" {
			continue
		}
		if code := CodeForStatus(status); !seen[code] {
			t.Errorf("CodeForStatus(%d) = %q is not cataloged", status, code)
		}
	}
	if _, ok := LookupErrorCode("nope"); ok {
		t.Fatal("LookupErrorCode found an unknown code")
	}
	if c, ok := LookupErrorCode(CodeNotFound); !ok || c.Status != 404 {
		t.Fatal("LookupErrorCode(not_found)")
	}
}

// TestErrorCodesCatalogued parses this package and fails when a Code*
// constant or a string literal passed as an error code (Conflict, NewError,
// Unavailable) is missing from ErrorCodes().
func TestErrorCodesCatalogued(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ValueSpec:
				for i, id := range n.Names {
					if strings.HasPrefix(id.Name, "Code") && i < len(n.Values) {
						if lit, ok := n.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							v, _ := strconv.Unquote(lit.Value)
							used[v] = fset.Position(id.Pos()).String()
						}
					}
				}
			case *ast.CallExpr:
				fn, ok := n.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				idx := map[string]int{"Conflict": 0, "Unavailable": 0, "NewError": 1}
				i, ok := idx[fn.Name]
				if !ok || len(n.Args) <= i {
					return true
				}
				if lit, ok := n.Args[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					used[v] = fset.Position(lit.Pos()).String()
				}
			}
			return true
		})
	}
	if len(used) < 10 {
		t.Fatalf("scan found only %d codes; the scanner is broken", len(used))
	}
	for code, where := range used {
		if _, ok := LookupErrorCode(code); !ok {
			t.Errorf("%s: error code %q is not in ErrorCodes() (errorcodes.go) and docs/api/errors.md", where, code)
		}
	}
}

// TestErrorCatalogDocumented keeps docs/api/errors.md in sync: every code
// has a table row "| `code` | status | retryable |".
func TestErrorCatalogDocumented(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "api", "errors.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, c := range ErrorCodes() {
		retry := "no"
		if c.Retryable {
			retry = "yes"
		}
		row := fmt.Sprintf("| `%s` | %d | %s |", c.Code, c.Status, retry)
		if !strings.Contains(doc, row) {
			t.Errorf("docs/api/errors.md lacks the row %q", row)
		}
	}
	rows := regexp.MustCompile("(?m)^\\| `([a-z0-9_]+)` \\| \\d{3} \\|").FindAllStringSubmatch(doc, -1)
	for _, m := range rows {
		if _, ok := LookupErrorCode(m[1]); !ok {
			t.Errorf("docs/api/errors.md documents %q, which is not in ErrorCodes()", m[1])
		}
	}
}
