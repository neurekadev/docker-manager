package managermove

import (
	"errors"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// TestNormalizeServerAddress: IP addresses and host names, with or
// without a port (8080 by default) or an http:// prefix, become host:port;
// loopback, wildcard, https, paths and junk are refused with the field.
func TestNormalizeServerAddress(t *testing.T) {
	for raw, want := range map[string]string{
		"192.168.1.10":             "192.168.1.10:8080",
		" 192.168.1.10:9000 ":      "192.168.1.10:9000",
		"http://192.168.1.10:8080": "192.168.1.10:8080",
		"http://NAS.lan/":          "nas.lan:8080",
		"nas.lan":                  "nas.lan:8080",
		"fd00::10":                 "[fd00::10]:8080",
		"[fd00::10]":               "[fd00::10]:8080",
		"[fd00::10]:8081":          "[fd00::10]:8081",
		"::ffff:192.168.1.10":      "192.168.1.10:8080",
	} {
		got, err := NormalizeServerAddress("newServerAddress", raw)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "127.0.0.1", "0.0.0.0", "localhost", "https://192.168.1.10", "http://192.168.1.10/path",
		"http://user@192.168.1.10", "192.168.1.10:0", "192.168.1.10:99999", "nas_lan", "-bad.example"} {
		_, err := NormalizeServerAddress("thisServerAddress", raw)
		var fe *domain.FieldError
		if !errors.As(err, &fe) || fe.Field != "thisServerAddress" {
			t.Errorf("%q: %v", raw, err)
		}
	}
}
