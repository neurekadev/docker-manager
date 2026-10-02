package s3probe

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrAddressNotAllowed refuses a connection to an address the probe never
// contacts: loopback, link-local (cloud metadata), unspecified and
// multicast addresses. Private ranges stay allowed (MinIO on the LAN or a
// Docker network is a normal destination).
var ErrAddressNotAllowed = errors.New("this address is not allowed")

// ClassAddressNotAllowed is the class of a refused address.
const ClassAddressNotAllowed = "address_not_allowed"

// metadataV6 is the IPv6 address of the EC2 instance metadata service.
var metadataV6 = netip.MustParseAddr("fd00:ec2::254")

// thisNetwork is 0.0.0.0/8, which Linux connects to the local host.
var thisNetwork = netip.MustParsePrefix("0.0.0.0/8")

// AllowedAddr reports whether the probe may connect to ip.
func AllowedAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	switch {
	case !ip.IsValid(), ip.IsUnspecified(), ip.IsLoopback(), ip.IsLinkLocalUnicast(), ip.IsMulticast(),
		ip == metadataV6, ip.Is4() && thisNetwork.Contains(ip):
		return false
	}
	return true
}

// control checks the address a connection is about to use, after DNS
// resolution: a name that resolves (or rebinds) to a refused address is
// refused at connect time, and so is every redirect target.
func control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !AllowedAddr(ap.Addr()) {
		return ErrAddressNotAllowed
	}
	return nil
}

// NewClient returns the HTTP client of S3 probes: it connects directly
// (no proxy from the environment, so the address check sees the endpoint
// itself), refuses the addresses AllowedAddr rejects and never follows
// redirects (S3 does not send any the probe needs).
func NewClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, Control: control}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = dialer.DialContext
	return &http.Client{Transport: tr, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
