package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func fieldOf(err error) string {
	var fe *FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

func TestUpdateExcluded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"absent", nil, false},
		{"enabled", map[string]string{LabelUpdateExclude: "true"}, true},
		{"case insensitive", map[string]string{LabelUpdateExclude: "TRUE"}, true},
		{"disabled", map[string]string{LabelUpdateExclude: "false"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := UpdateExcluded(tc.labels); got != tc.want {
				t.Fatalf("UpdateExcluded() = %t, want %t", got, tc.want)
			}
		})
	}
	if err := ValidateLabels("labels", map[string]string{LabelUpdateExclude: "true"}); err != nil {
		t.Fatalf("update exclusion label must be user-settable: %v", err)
	}
}

// TestBackupAndMaintenanceExcluded: the user-set exclusion labels read
// "true" in any case, and users may set them.
func TestBackupAndMaintenanceExcluded(t *testing.T) {
	for _, fn := range []struct {
		label string
		read  func(map[string]string) bool
	}{{LabelBackupExclude, BackupExcluded}, {LabelMaintenanceExclude, MaintenanceExcluded}} {
		if fn.read(nil) || fn.read(map[string]string{fn.label: "false"}) || !fn.read(map[string]string{fn.label: "True"}) {
			t.Errorf("%s is not read as true only", fn.label)
		}
		if err := ValidateLabels("labels", map[string]string{fn.label: "true"}); err != nil {
			t.Errorf("%s must be user-settable: %v", fn.label, err)
		}
	}
	for name, want := range map[string]bool{"buildx_buildkit_builder0_state": true, "buildx_buildkit_state": false, "app_state": false, "buildx_cache": false} {
		if IsBuildxVolume(name) != want {
			t.Errorf("IsBuildxVolume(%q) = %t", name, !want)
		}
	}
}

// TestIsHelperContainer: Docker Manager's set-aside containers (update,
// rename), Compose's temporary replacement (label and temporary name) and
// the self-update helper are temporary; look-alikes are not.
func TestIsHelperContainer(t *testing.T) {
	replace := map[string]string{ComposeReplaceLabel: "0123456789abcdef"}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"web-docker-manager-update-0123456789ab", nil, true},
		{"/web-docker-manager-update-0123456789ab", nil, true},
		{"app-db-1-docker-manager-rename-abcdef012345", map[string]string{ComposeProjectLabel: "old"}, true},
		{"0123456789ab_app-db-1", replace, true},
		{"/0123456789ab_app-db-1", replace, true},
		{"helper", map[string]string{LabelRole: RoleSelfUpdate}, true},
		// Look-alikes.
		{"web-docker-manager-update-0123456789AB", nil, false},   // upper case
		{"web-docker-manager-update-0123456789a", nil, false},    // 11 digits
		{"web-docker-manager-update-0123456789abc", nil, false},  // 13 digits
		{"web-docker-manager-update-0123456789ab-x", nil, false}, // not at the end
		{"-docker-manager-update-0123456789ab", nil, false},      // no original name
		{"web-docker-manager-backup-0123456789ab", nil, false},
		{"app-db-1", replace, false},              // Compose keeps the label after the rename
		{"0123456789ab_app-db-1", nil, false},     // the name alone
		{"0123456789xy_app-db-1", replace, false}, // not hex
		{"0123456789ab_", replace, false},         // nothing after the ID
		{"agent", map[string]string{LabelRole: "agent"}, false},
		{"", nil, false},
	} {
		if got := IsHelperContainer(tc.name, tc.labels); got != tc.want {
			t.Errorf("IsHelperContainer(%q, %v) = %t, want %t", tc.name, tc.labels, got, tc.want)
		}
	}
}

// TestContainerSpecValidation: the v1 create form accepts the common
// options and refuses reserved labels, the Docker socket, malformed ports,
// mounts, networks and environment entries (without echoing values).
func TestContainerSpecValidation(t *testing.T) {
	pids := int64(10)
	good := ContainerSpec{Name: "web", Image: "ghcr.io/org/app:1.2", Command: []string{"serve"}, Env: []string{"A=1", "TOKEN=s3cret"},
		Labels: map[string]string{"team": "ops"}, WorkingDir: "/app", Ports: []PortSpec{{ContainerPort: 80, HostPort: 8080, HostIP: "127.0.0.1"},
			{ContainerPort: 53, Protocol: "udp", HostIP: "::1"}},
		Mounts:   []MountSpec{{Type: "bind", Source: "/srv/www", Target: "/www", ReadOnly: true}, {Type: "volume", Source: "data", Target: "/data"}, {Type: "volume", Target: "/anon"}, {Type: "tmpfs", Target: "/tmp"}},
		Networks: []NetworkAttachment{{Name: "bridge"}, {Name: "backend", Aliases: []string{"api"}}}, RestartPolicy: "unless-stopped",
		Resources:   ResourcesSpec{NanoCPUs: 1e9, Memory: 64 << 20, MemorySwap: 128 << 20, PidsLimit: &pids},
		Healthcheck: &HealthcheckSpec{Test: []string{"CMD", "/check"}, Retries: 3}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for field, mutate := range map[string]func(*ContainerSpec){
		"name":                   func(s *ContainerSpec) { s.Name = "-bad" },
		"image":                  func(s *ContainerSpec) { s.Image = "Bad Image" },
		"env[1]":                 func(s *ContainerSpec) { s.Env = []string{"A=1", "no equals"} },
		"labels":                 func(s *ContainerSpec) { s.Labels = map[string]string{LabelRole: "manager"} },
		"workingDir":             func(s *ContainerSpec) { s.WorkingDir = "relative" },
		"ports[0].containerPort": func(s *ContainerSpec) { s.Ports = []PortSpec{{}} },
		"ports[0].protocol":      func(s *ContainerSpec) { s.Ports = []PortSpec{{ContainerPort: 1, Protocol: "sctp"}} },
		"ports[0].hostIp":        func(s *ContainerSpec) { s.Ports = []PortSpec{{ContainerPort: 1, HostIP: "256.1.1.1"}} },
		"mounts[0].target":       func(s *ContainerSpec) { s.Mounts = []MountSpec{{Type: "tmpfs", Target: "/"}} },
		"mounts[0].type":         func(s *ContainerSpec) { s.Mounts = []MountSpec{{Type: "npipe", Target: "/x"}} },
		"networks[1].name":       func(s *ContainerSpec) { s.Networks = []NetworkAttachment{{Name: "a"}, {Name: "a"}} },
		"restartPolicy":          func(s *ContainerSpec) { s.RestartPolicy = "sometimes" },
		"resources.memoryBytes":  func(s *ContainerSpec) { s.Resources.Memory = 1024 },
		"healthcheck.test":       func(s *ContainerSpec) { s.Healthcheck = &HealthcheckSpec{Test: []string{"curl"}} },
	} {
		s := good
		mutate(&s)
		err := s.Validate()
		if fieldOf(err) != field {
			t.Errorf("%s: %v", field, err)
		}
		if err != nil && strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: error echoes an env value: %v", field, err)
		}
	}
	// Compose labels are reserved too; host/none stand alone.
	s := good
	s.Labels = map[string]string{ComposeProjectLabel: "shop"}
	if fieldOf(s.Validate()) != "labels" {
		t.Error("compose label accepted")
	}
	s = good
	s.Networks = []NetworkAttachment{{Name: "host"}, {Name: "backend"}}
	if fieldOf(s.Validate()) != "networks[0].name" {
		t.Error("host combined with another network")
	}
}

// TestDockerSocketBindsRefused: the socket, a directory containing it, or
// any docker.sock path cannot be bound into a created container.
func TestDockerSocketBindsRefused(t *testing.T) {
	for _, src := range []string{"/var/run/docker.sock", "/run/docker.sock", "/var/run", "/run", "/", "/var", "/home/u/docker.sock"} {
		s := ContainerSpec{Name: "x", Image: "nginx:1.27", Mounts: []MountSpec{{Type: "bind", Source: src, Target: "/s"}}}
		if fieldOf(s.Validate()) != "mounts[0].source" {
			t.Errorf("bind of %s accepted", src)
		}
	}
	for _, src := range []string{"/srv/data", "/var/lib/app", "/runner"} {
		s := ContainerSpec{Name: "x", Image: "nginx:1.27", Mounts: []MountSpec{{Type: "bind", Source: src, Target: "/s"}}}
		if err := s.Validate(); err != nil {
			t.Errorf("bind of %s refused: %v", src, err)
		}
	}
}

func TestCreateInputsValidation(t *testing.T) {
	in := ContainerCreateInput{Spec: ContainerSpec{Name: "x", Image: "nginx"}, Ownership: map[string]string{LabelManaged: ManagedStandalone, LabelSpec: "s"}}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	in.Ownership[LabelRole] = "manager"
	if fieldOf(in.Validate()) != "ownership" {
		t.Error("role label accepted as ownership")
	}
	for _, c := range []struct {
		err   error
		field string
	}{
		{VolumeCreateInput{Name: "v", Driver: "local"}.Validate(), ""},
		{VolumeCreateInput{Name: "bad name"}.Validate(), "name"},
		{VolumeCreateInput{Name: "v", Labels: map[string]string{LabelSpec: "x"}}.Validate(), "labels"},
		{NetworkCreateInput{Name: "n", Driver: "bridge"}.Validate(), ""},
		{NetworkCreateInput{Name: "bridge"}.Validate(), "name"},
		{ImagePullInput{Reference: "ghcr.io/org/app:1@sha256:" + strings.Repeat("a", 64)}.Validate(), ""},
		{ImagePullInput{Reference: "registry.example.com:5000/team/app"}.Validate(), ""},
		{ImagePullInput{Reference: "UPPER/case"}.Validate(), "reference"},
		{ImagePullInput{Reference: "nginx", Platform: "linux"}.Validate(), "platform"},
		{ValidateTagTarget("mirror/nginx:stable"), ""},
		{ValidateTagTarget("mirror/nginx@sha256:" + strings.Repeat("b", 64)), "target"},
	} {
		if fieldOf(c.err) != c.field {
			t.Errorf("%v, want field %q", c.err, c.field)
		}
	}
	if !ValidImageID("sha256:"+strings.Repeat("c", 64)) || !ValidImageID(strings.Repeat("c", 12)) || ValidImageID("nginx") {
		t.Error("image IDs")
	}
}

// TestOptionalInspectFields: the restart policy's retry count and the
// attached containers' addresses are optional additions: absent when
// unset, and older agents' answers without them still decode.
func TestOptionalInspectFields(t *testing.T) {
	raw, err := json.Marshal(ContainerDetails{RestartPolicy: "always"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "restartMaxRetries") {
		t.Errorf("unset retries sent: %s", raw)
	}
	raw, _ = json.Marshal(ContainerDetails{RestartPolicy: "on-failure", RestartMaxRetries: 3})
	var d ContainerDetails
	if err := json.Unmarshal(raw, &d); err != nil || d.RestartMaxRetries != 3 {
		t.Fatalf("round trip %+v, %v", d, err)
	}
	raw, _ = json.Marshal(ContainerRef{ID: "abc", Name: "web"})
	if string(raw) != `{"id":"abc","name":"web"}` {
		t.Errorf("container ref without addresses: %s", raw)
	}
	var n NetworkInfo
	if err := json.Unmarshal([]byte(`{"id":"n1","name":"shop_default","driver":"bridge","containers":[{"id":"abc","name":"web"}]}`), &n); err != nil ||
		n.Containers[0].IPAddress != "" {
		t.Fatalf("older agent's network %+v, %v", n, err)
	}
	raw, _ = json.Marshal(ContainerRef{ID: "abc", Name: "web", IPAddress: "172.20.0.3", IPv6Address: "fd00::3"})
	if !strings.Contains(string(raw), `"ipAddress":"172.20.0.3"`) || !strings.Contains(string(raw), `"ipv6Address":"fd00::3"`) {
		t.Errorf("container ref with addresses: %s", raw)
	}
}

// TestLabelKeys: Docker Manager's labels use the docker-manager. prefix;
// every label that existed under the legacy prefix maps to it.
func TestLabelKeys(t *testing.T) {
	for key, legacy := range map[string]string{
		LabelRole: "dev.neureka.docker-manager.role", LabelManaged: "dev.neureka.docker-manager.managed",
		LabelInstance: "dev.neureka.docker-manager.instance", LabelSpec: "dev.neureka.docker-manager.spec",
		LabelMigration: "dev.neureka.docker-manager.migration", LabelDescription: "dev.neureka.docker-manager.description",
		LabelDependsOn: "dev.neureka.docker-manager.depends_on",
	} {
		if !strings.HasPrefix(key, "docker-manager.") || LegacyLabel(key) != legacy || LegacyLabelRenames()[legacy] != key {
			t.Errorf("%s: legacy %q", key, LegacyLabel(key))
		}
	}
	for _, k := range UserLabels {
		if LegacyLabel(k) != "" {
			t.Errorf("user label %s has a legacy key", k)
		}
	}
}

// TestLookupLabel: readers accept the current key, the legacy key, and
// prefer the current key when an object carries both.
func TestLookupLabel(t *testing.T) {
	legacy := LegacyLabelPrefix + "role"
	for _, tc := range []struct {
		labels map[string]string
		want   string
		ok     bool
	}{
		{map[string]string{LabelRole: "agent"}, "agent", true},
		{map[string]string{legacy: "manager"}, "manager", true},
		{map[string]string{LabelRole: "agent", legacy: "manager"}, "agent", true},
		{map[string]string{LabelRole: ""}, "", true},
		{map[string]string{"role": "agent"}, "", false},
		{nil, "", false},
	} {
		if v, ok := LookupLabel(tc.labels, LabelRole); v != tc.want || ok != tc.ok {
			t.Errorf("LookupLabel(%v) = %q, %t", tc.labels, v, ok)
		}
		if LabelValue(tc.labels, LabelRole) != tc.want {
			t.Errorf("LabelValue(%v)", tc.labels)
		}
	}
	if !HasRole(map[string]string{legacy: "agent"}, "agent") || HasRole(map[string]string{legacy: "agent"}, "manager") ||
		HasRole(nil, "") || !HasRole(map[string]string{LabelRole: "manager", legacy: "agent"}, "manager") {
		t.Error("HasRole")
	}
	// Labels without a legacy key are read under their key only.
	if LabelValue(map[string]string{LegacyLabelPrefix + "update.exclude": "true"}, LabelUpdateExclude) != "" {
		t.Error("an exclusion label has no legacy key")
	}
	if !IsHelperContainer("helper", map[string]string{legacy: RoleSelfUpdate}) {
		t.Error("legacy self-update helper not recognized")
	}
}

// TestCurrentAndLegacyLabels: recreated objects carry only current keys;
// the ownership for an agent of the previous version only legacy ones.
func TestCurrentAndLegacyLabels(t *testing.T) {
	in := map[string]string{LegacyLabelPrefix + "managed": ManagedStandalone, LegacyLabelPrefix + "spec": "old",
		LabelSpec: "new", "team": "ops", LabelUpdateExclude: "true"}
	got := CurrentLabels(in)
	want := map[string]string{LabelManaged: ManagedStandalone, LabelSpec: "new", "team": "ops", LabelUpdateExclude: "true"}
	if len(got) != len(want) {
		t.Fatalf("CurrentLabels = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("CurrentLabels[%s] = %q, want %q", k, got[k], v)
		}
	}
	if len(in) != 5 {
		t.Error("CurrentLabels changed its input")
	}
	old := LegacyLabels(map[string]string{LabelManaged: ManagedStandalone, LabelInstance: "i", "team": "ops"})
	if len(old) != 3 || old[LegacyLabelPrefix+"managed"] != ManagedStandalone || old[LegacyLabelPrefix+"instance"] != "i" || old["team"] != "ops" {
		t.Errorf("LegacyLabels = %v", old)
	}
	if CurrentLabels(nil) != nil || LegacyLabels(nil) != nil {
		t.Error("nil labels")
	}
}

// TestOwnLabels: both prefixes are Docker Manager's except the user-set
// exclusions; saved specifications lose Docker Manager's own labels.
func TestOwnLabels(t *testing.T) {
	for k, want := range map[string]bool{
		LabelRole: true, LabelDescription: true, "docker-manager.anything": true, LegacyLabelPrefix + "role": true,
		LegacyLabelPrefix + "update.exclude": true, LabelUpdateExclude: false, LabelBackupExclude: false,
		LabelMaintenanceExclude: false, "traefik.enable": false, ComposeProjectLabel: false, "docker-manager": false,
	} {
		if OwnLabel(k) != want {
			t.Errorf("OwnLabel(%q) = %t", k, !want)
		}
	}
	got := WithoutOwnLabels(map[string]string{"docker-manager.team": "ops", LabelBackupExclude: "true", "team": "ops"})
	if len(got) != 2 || got[LabelBackupExclude] != "true" || got["team"] != "ops" {
		t.Errorf("WithoutOwnLabels = %v", got)
	}
	if WithoutOwnLabels(map[string]string{LabelSpec: "x"}) != nil {
		t.Error("nothing left must be nil")
	}
}

// TestValidateLabelsReserved: users cannot set labels under either
// Docker Manager prefix or Compose's, except the three exclusions.
func TestValidateLabelsReserved(t *testing.T) {
	for _, k := range []string{LabelRole, LabelManaged, "docker-manager.custom", LabelDescription, LegacyLabelPrefix + "role",
		LegacyLabelPrefix + "backup.exclude", ComposeServiceLabel} {
		if fieldOf(ValidateLabels("labels", map[string]string{k: "x"})) != "labels" {
			t.Errorf("label %s accepted", k)
		}
	}
	ok := map[string]string{LabelUpdateExclude: "true", LabelBackupExclude: "true", LabelMaintenanceExclude: "true",
		"team": "ops", "dev.neureka.other": "x"}
	if err := ValidateLabels("labels", ok); err != nil {
		t.Error(err)
	}
	if err := (VolumeCreateInput{Name: "v", Labels: map[string]string{LabelBackupExclude: "true"}}).Validate(); err != nil {
		t.Error(err)
	}
	if err := (NetworkCreateInput{Name: "n", Labels: map[string]string{LabelMaintenanceExclude: "true"}}).Validate(); err != nil {
		t.Error(err)
	}
}

// TestOwnershipKeys: a create input carries ownership under the current
// or the legacy keys (a job queued for an agent before its upgrade), never
// other Docker Manager labels.
func TestOwnershipKeys(t *testing.T) {
	for _, own := range []map[string]string{
		{LabelManaged: ManagedStandalone, LabelSpec: "s", LabelInstance: "i"},
		LegacyLabels(map[string]string{LabelManaged: ManagedStandalone, LabelSpec: "s", LabelInstance: "i"}),
	} {
		in := ContainerCreateInput{Spec: ContainerSpec{Name: "x", Image: "nginx"}, Ownership: own}
		if err := in.Validate(); err != nil {
			t.Errorf("%v: %v", own, err)
		}
	}
	for _, k := range []string{LabelRole, LegacyLabelPrefix + "role", LabelMigration, LegacyLabelPrefix + "migration", "team"} {
		in := ContainerCreateInput{Spec: ContainerSpec{Name: "x", Image: "nginx"}, Ownership: map[string]string{k: "x"}}
		if fieldOf(in.Validate()) != "ownership" {
			t.Errorf("%s accepted as ownership", k)
		}
	}
}

// TestMigratedVolumeLabels: a migrated volume keeps Compose's labels and
// the user-set exclusions, never Docker Manager's own labels (the
// destination sets its own LabelMigration).
func TestMigratedVolumeLabels(t *testing.T) {
	in := func(labels map[string]string) error {
		return MigrationReceiveInput{MigrationID: "0190a6e0-0000-7000", Part: PartVolume,
			Volume: &MigrationVolumeSpec{Name: "shop_db", Labels: labels}}.Validate()
	}
	if err := in(map[string]string{ComposeProjectLabel: "shop", LabelBackupExclude: "true"}); err != nil {
		t.Error(err)
	}
	for _, k := range []string{LabelMigration, LegacyLabelPrefix + "migration", "docker-manager.custom"} {
		if in(map[string]string{k: "x"}) == nil {
			t.Errorf("%s accepted", k)
		}
	}
}
