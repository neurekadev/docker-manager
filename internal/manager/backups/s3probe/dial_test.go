package s3probe

import (
	"errors"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

func TestAllowedAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1":        false,
		"127.8.9.10":       false,
		"::1":              false,
		"::ffff:127.0.0.1": false,
		"0.0.0.0":          false,
		"0.1.2.3":          false,
		"::":               false,
		"169.254.169.254":  false,
		"169.254.0.1":      false,
		"fe80::1":          false,
		"fd00:ec2::254":    false,
		"224.0.0.1":        false,
		"ff02::1":          false,
		"10.0.0.5":         true,
		"172.18.0.3":       true,
		"192.168.1.20":     true,
		"fd00::5":          true,
		"203.0.113.7":      true,
		"2001:db8::7":      true,
	} {
		if got := AllowedAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("AllowedAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

// TestControlChecksTheResolvedAddress: the dialer's check runs on the
// address being connected to (after DNS), so a name that resolves to a
// refused address is refused too.
func TestControlChecksTheResolvedAddress(t *testing.T) {
	for addr, want := range map[string]error{
		"127.0.0.1:9000":       ErrAddressNotAllowed,
		"[fe80::1%eth0]:80":    ErrAddressNotAllowed,
		"169.254.169.254:80":   ErrAddressNotAllowed,
		"[fd00:ec2::254]:80":   ErrAddressNotAllowed,
		"not-an-address:80":    ErrAddressNotAllowed,
		"10.0.0.5:9000":        nil,
		"[2001:db8::7]:443":    nil,
		"[::ffff:10.0.0.5]:80": nil,
	} {
		if err := control("tcp", addr, nil); !errors.Is(err, want) {
			t.Errorf("control(%s) = %v, want %v", addr, err, want)
		}
	}
}

func TestNewClientFollowsNoRedirect(t *testing.T) {
	c := NewClient(time.Second)
	if c.Timeout != time.Second || c.CheckRedirect == nil {
		t.Fatalf("client = %+v", c)
	}
	if err := c.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("CheckRedirect = %v", err)
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || tr.Proxy != nil || tr.DialContext == nil {
		t.Errorf("transport = %+v", c.Transport)
	}
}
