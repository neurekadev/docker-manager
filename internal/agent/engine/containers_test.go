package engine

import (
	"net/http"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestInspectContainerRestartPolicy: the inspected restart policy carries
// the on-failure policy's maximum retry count; other policies (and an
// unlimited on-failure policy) report none.
func TestInspectContainerRestartPolicy(t *testing.T) {
	cases := []struct {
		name, policy string
		retries      int
		want         int
	}{
		{"on-failure with retries", "on-failure", 5, 5},
		{"on-failure unlimited", "on-failure", 0, 0},
		{"always", "always", 0, 0},
		{"unless-stopped", "unless-stopped", 0, 0},
		{"no", "no", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := enginetest.Start(t, enginetest.Options{})
			fake.Handle(http.MethodGet, "/containers/api/json", func(w http.ResponseWriter, _ *http.Request) {
				enginetest.JSON(w, http.StatusOK, map[string]any{
					"Id": "0123456789abcdef", "Name": "/api", "Image": "sha256:img",
					"Config":     map[string]any{"Image": "nginx:1.27"},
					"HostConfig": map[string]any{"RestartPolicy": map[string]any{"Name": tc.policy, "MaximumRetryCount": tc.retries}},
				})
			})
			d, err := connect(t, fake).InspectContainer(testutil.Context(t), "api")
			if err != nil {
				t.Fatal(err)
			}
			if d.RestartPolicy != tc.policy || d.RestartMaxRetries != tc.want {
				t.Fatalf("restart policy %q max %d, want %q max %d", d.RestartPolicy, d.RestartMaxRetries, tc.policy, tc.want)
			}
		})
	}
}
