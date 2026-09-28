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
