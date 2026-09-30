package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/neurekadev/docker-manager"

// forbiddenCalls are package-level functions that open listening sockets.
var forbiddenCalls = map[string]map[string]bool{
	"net":      {"Listen": true, "ListenTCP": true, "ListenUDP": true, "ListenIP": true, "ListenUnix": true, "ListenUnixgram": true, "ListenPacket": true, "ListenMulticastUDP": true, "FileListener": true},
	"net/http": {"ListenAndServe": true, "ListenAndServeTLS": true, "Serve": true, "ServeTLS": true},
}

// forbiddenMethods are method names that start servers on any receiver.
var forbiddenMethods = map[string]bool{"ListenAndServe": true, "ListenAndServeTLS": true, "ServeTLS": true}

// forbiddenImports must not be linked into the agent at all.
var forbiddenImports = []string{"net/http/httptest", module + "/internal/manager"}

// TestAgentNeverListens statically checks every in-module package linked
// into docker-agent (non-test files) for code that opens a listener. The
// agent is outbound-only (#27, #28).
func TestAgentNeverListens(t *testing.T) {
	root := filepath.Join("..", "..")
	pkgs := agentPackages(t, root)
	if len(pkgs) < 3 {
		t.Fatalf("found only %v; import walk is broken", pkgs)
	}
	for _, pkg := range pkgs {
		dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(pkg, module), "/")))
		for _, f := range parseDir(t, dir) {
			for _, p := range checkFile(f.name, f.file) {
				t.Errorf("%s: the agent must never open a listening socket", p)
			}
		}
	}
}

type parsed struct {
	name string
	file *ast.File
}

func parseDir(t *testing.T, dir string) []parsed {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []parsed
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		p := filepath.Join(dir, n)
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, parsed{name: p, file: f})
	}
	return out
}

// agentPackages follows in-module imports from cmd/docker-agent.
func agentPackages(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	queue := []string{module + "/cmd/docker-agent"}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(pkg, module), "/")))
		for _, f := range parseDir(t, dir) {
			for _, imp := range f.file.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				for _, bad := range forbiddenImports {
					if path == bad || strings.HasPrefix(path, bad+"/") {
						t.Errorf("%s imports %s, which the agent must not link", f.name, path)
					}
				}
				if strings.HasPrefix(path, module+"/") {
					queue = append(queue, path)
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// checkFile returns the listener-opening constructs in f.
func checkFile(name string, f *ast.File) []string {
	var problems []string
	aliases := map[string]string{} // local name -> import path
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		local := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		aliases[local] = path
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			path := aliases[id.Name]
			if fns, ok := forbiddenCalls[path]; ok && fns[sel.Sel.Name] {
				problems = append(problems, name+" uses "+path+"."+sel.Sel.Name)
			}
			if path == "net" && sel.Sel.Name == "ListenConfig" {
				problems = append(problems, name+" uses net.ListenConfig")
			}
		}
		if forbiddenMethods[sel.Sel.Name] {
			problems = append(problems, name+" calls ."+sel.Sel.Name)
		}
		return true
	})
	return problems
}

func TestCheckFileDetectsListeners(t *testing.T) {
	src := `package x
import (
	stdnet "net"
	"net/http"
)
func f() {
	stdnet.Listen("tcp", ":1")
	http.ListenAndServe(":1", nil)
	var s http.Server
	s.ListenAndServeTLS("", "")
	var lc stdnet.ListenConfig
	_ = lc
	http.Get("https://example.com")
}`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkFile("x.go", f); len(got) != 5 {
		t.Fatalf("detected %d problems, want 5: %v", len(got), got)
	}
}
