package jobspec

import (
	"errors"
	"slices"
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
)

func TestCapabilities(t *testing.T) {
	stackT := []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}, {Type: domain.TargetPath, ID: "/a"}}
	volumeT := []domain.JobTarget{{Type: domain.TargetVolume, ID: "data"}, {Type: domain.TargetPath, ID: "/a"}}
	cases := []struct {
		kind    domain.JobKind
		targets []domain.JobTarget
		input   string
		want    []string
		invalid bool
	}{
		{ContainerRestart, []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}, `{}`, []string{"container.restart"}, false},
		{FilesCopy, stackT, `{}`, []string{"stack.files.copy"}, false},
		{FilesCopy, volumeT, `{}`, []string{"volume.files.copy"}, false},
		{FilesDelete, []domain.JobTarget{{Type: domain.TargetPath, ID: "/a"}}, `{}`, nil, true},
		{FilesMove, append(stackT, volumeT...), `{}`, nil, true},
		{FilesMetadata, stackT, `{"chmod":{"mode":"0644"}}`, []string{"stack.files.chmod"}, false},
		{FilesMetadata, volumeT, `{"chown":{"uid":0},"chmod":{"mode":"0600"},"recursive":true}`, []string{"volume.files.chmod", "volume.files.chown"}, false},
		{FilesMetadata, stackT, `{"recursive":true}`, nil, true},
		{FilesMetadata, stackT, `{"chmod":null}`, nil, true},
		{FilesMetadata, stackT, `[]`, nil, true},
	}
	for _, c := range cases {
		spec, _ := Lookup(c.kind)
		got, err := spec.Capabilities(c.targets, []byte(c.input))
		if c.invalid {
			if !errors.Is(err, domain.ErrJobInvalid) {
				t.Errorf("%s %v %s: %v %v, want invalid", c.kind, c.targets, c.input, got, err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s %s: %v %v, want %v", c.kind, c.input, got, err, c.want)
		}
	}
	meta, _ := Lookup(FilesMetadata)
	if got := meta.PossibleCapabilities(); !slices.Equal(got, []string{"stack.files.chmod", "stack.files.chown", "volume.files.chmod", "volume.files.chown"}) {
		t.Fatalf("possible %v", got)
	}
	if got := meta.capabilityDoc(); got != "`{stack,volume}.files.chmod`, `{stack,volume}.files.chown`" {
		t.Fatalf("doc %q", got)
	}
	pull, _ := Lookup(ImagePull)
	if got := pull.PossibleCapabilities(); !slices.Equal(got, []string{"image.pull"}) || pull.capabilityDoc() != "`image.pull`" {
		t.Fatalf("image.pull %v", got)
	}
	bad := meta
	bad.CapabilityByInput = map[string]string{"chmod": "Chmod"}
	if bad.Validate() == nil {
		t.Fatal("invalid input capability accepted")
	}
}
