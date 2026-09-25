package eligible

import (
	"os"
	"testing"

	"go.yaml.in/yaml/v4"
)

type corpusCase struct {
	Name  string `yaml:"name"`
	Ref   string `yaml:"ref"`
	Build bool   `yaml:"build"`
	Pull  string `yaml:"pull"`
	Want  string `yaml:"want"`
	Warn  bool   `yaml:"warn"`
}

func TestEligibilityCorpus(t *testing.T) {
	b, err := os.ReadFile("testdata/corpus.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cases []corpusCase
	if err := yaml.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 20 {
		t.Fatalf("corpus has %d cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			r := Check(Subject{Reference: c.Ref, Build: c.Build, PullPolicy: c.Pull})
			got := r.Reason
			if r.Eligible {
				got = "eligible"
			}
			if got != c.Want {
				t.Fatalf("got %q (%s), want %q", got, r.Message, c.Want)
			}
			if r.NonVersionTag != c.Warn {
				t.Errorf("non-version warning %v, want %v", r.NonVersionTag, c.Warn)
			}
			if !r.Eligible && r.Message == "" {
				t.Error("an ineligible definition needs a visible reason")
			}
		})
	}
}
