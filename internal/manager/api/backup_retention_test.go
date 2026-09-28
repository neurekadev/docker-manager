package api

import (
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
)

// TestDeprecatedMinKeepFoldsIntoLast: the former minimum recovery floor
// did what "last" does, so a request still sending it raises last to it
// when rules are set, and changes nothing without rules (everything is
// kept anyway). Responses never carry it.
func TestDeprecatedMinKeepFoldsIntoLast(t *testing.T) {
	cases := []struct {
		name string
		in   BackupRetention
		want int
	}{
		{"floor above last", BackupRetention{Daily: 7, MinKeep: 3}, 3},
		{"last already higher", BackupRetention{Last: 10, MinKeep: 3}, 10},
		{"no rules: keep everything", BackupRetention{MinKeep: 3}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toRetention(&c.in).Last; got != c.want {
				t.Errorf("last = %d, want %d", got, c.want)
			}
		})
	}
	out := newBackupPolicy(domain.BackupPolicy{Retention: domain.BackupRetention{Daily: 7, Last: 3}}, authz.View{Level: authz.Full})
	if out.Retention == nil || out.Retention.MinKeep != 0 || out.Retention.Last != 3 {
		t.Errorf("response retention %+v", out.Retention)
	}
}
