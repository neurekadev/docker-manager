package registries

import (
	"errors"
	"os"
	"slices"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/imageref"
)

type corpusConn struct {
	Host        string `yaml:"host"`
	Pattern     string `yaml:"pattern"`
	Environment string `yaml:"environment"`
	Stack       string `yaml:"stack"`
	Priority    int    `yaml:"priority"`
	Status      string `yaml:"status"`
}

type corpusCase struct {
	Name        string   `yaml:"name"`
	Ref         string   `yaml:"ref"`
	Environment string   `yaml:"environment"`
	Stack       string   `yaml:"stack"`
	Explicit    string   `yaml:"explicit"`
	Select      string   `yaml:"select"`
	Ambiguous   []string `yaml:"ambiguous"`
	Mismatch    bool     `yaml:"mismatch"`
	Candidates  []string `yaml:"candidates"`
}

type corpus struct {
	Connections map[string]corpusConn `yaml:"connections"`
	Cases       []corpusCase          `yaml:"cases"`
}

func loadCorpus(t *testing.T) ([]domain.RegistryConnection, []corpusCase) {
	t.Helper()
	b, err := os.ReadFile("testdata/matching.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c corpus
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	var conns []domain.RegistryConnection
	for id, cc := range c.Connections {
		host, err := imageref.NormalizeHost(cc.Host)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		p, err := imageref.ParsePattern(host, cc.Pattern)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		st := domain.RegistryConnectionActive
		if cc.Status == "revoked" {
			st = domain.RegistryConnectionRevoked
		}
		conns = append(conns, domain.RegistryConnection{ID: id, Name: id, Host: host, RepositoryPattern: string(p),
			EnvironmentID: cc.Environment, StackID: cc.Stack, Priority: cc.Priority, Status: st})
	}
	return conns, c.Cases
}

// TestMatchingCorpus checks the deterministic matching rule against the
// corpus: Docker Hub aliases, GHCR namespaces, self-hosted host:port,
// bindings, priorities, ambiguity and explicit selection.
func TestMatchingCorpus(t *testing.T) {
	conns, cases := loadCorpus(t)
	if len(cases) < 20 {
		t.Fatalf("corpus has only %d cases", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			ref, err := imageref.Parse(tc.Ref)
			if err != nil {
				t.Fatal(err)
			}
			req := domain.RegistrySelectRequest{Reference: tc.Ref, EnvironmentID: tc.Environment, StackID: tc.Stack, ConnectionID: tc.Explicit}
			// The input order must not matter.
			for _, order := range [][]domain.RegistryConnection{conns, reversed(conns), sorted(conns)} {
				sel, tied, err := Match(order, ref, req)
				switch {
				case tc.Mismatch:
					if !errors.Is(err, domain.ErrRegistryConnectionMismatch) {
						t.Fatalf("err = %v, want mismatch", err)
					}
					continue
				case err != nil:
					t.Fatal(err)
				case tc.Ambiguous != nil:
					if !slices.Equal(tied, tc.Ambiguous) || sel.Selected != nil {
						t.Fatalf("tied %v selected %v, want ambiguous %v", tied, sel.Selected, tc.Ambiguous)
					}
				case tc.Select == "anonymous":
					if sel.Selected != nil || len(tied) > 0 || !sel.Anonymous() {
						t.Fatalf("selected %+v tied %v, want anonymous", sel.Selected, tied)
					}
				default:
					if sel.Selected == nil || sel.Selected.ID != tc.Select || len(tied) > 0 {
						t.Fatalf("selected %+v tied %v, want %s", sel.Selected, tied, tc.Select)
					}
					if sel.Explicit != (tc.Explicit != "") {
						t.Fatalf("explicit = %v", sel.Explicit)
					}
				}
				if tc.Candidates != nil {
					var got []string
					for _, c := range sel.Candidates {
						got = append(got, c.Connection.ID)
					}
					if !slices.Equal(got, tc.Candidates) {
						t.Fatalf("candidates %v, want %v", got, tc.Candidates)
					}
				}
				if sel.Host != ref.Host || sel.Reference != ref.String() {
					t.Fatalf("selection %+v", sel)
				}
			}
		})
	}
}

func reversed(in []domain.RegistryConnection) []domain.RegistryConnection {
	out := slices.Clone(in)
	slices.Reverse(out)
	return out
}

func sorted(in []domain.RegistryConnection) []domain.RegistryConnection {
	out := slices.Clone(in)
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}
