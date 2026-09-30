package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// classify reduces a failed send to a stable error class. It prefers what
// the adapter saw itself (DNS, dial and TLS errors, refused redirects, the
// last HTTP status) and falls back to the kind of error for services that
// do not use HTTP (SMTP). The error text is only inspected here, never
// returned, logged or stored: it may contain the address.
func classify(ctx context.Context, err error, p *probe) string {
	status, redirect, _, panicked, netErr := p.snapshot()
	switch {
	case redirect:
		return domain.NotifyErrRedirect
	case panicked:
		return domain.NotifyErrRejected
	}
	if netErr != nil {
		if c := classOfNetErr(netErr); c != "" {
			return c
		}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return domain.NotifyErrAuth
	case status >= 500:
		return domain.NotifyErrHTTP5xx
	case status >= 400:
		return domain.NotifyErrHTTP4xx
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return domain.NotifyErrTimeout
	}
	if c := classOfNetErr(err); c != "" {
		return c
	}
	return classOfText(strings.ToLower(err.Error()))
}

// classOfNetErr classifies a transport, dial or TLS error ("" when it is
// none of them).
func classOfNetErr(err error) string {
	var (
		dnsErr   *net.DNSError
		certErr  *tls.CertificateVerificationError
		recErr   tls.RecordHeaderError
		authErr  x509.UnknownAuthorityError
		hostErr  x509.HostnameError
		invalid  x509.CertificateInvalidError
		opErr    *net.OpError
		netError net.Error
	)
	switch {
	case errors.As(err, &dnsErr):
		return domain.NotifyErrDNS
	case errors.As(err, &certErr), errors.As(err, &recErr), errors.As(err, &authErr), errors.As(err, &hostErr),
		errors.As(err, &invalid):
		return domain.NotifyErrTLS
	case errors.As(err, &netError) && netError.Timeout():
		return domain.NotifyErrTimeout
	case errors.Is(err, context.DeadlineExceeded):
		return domain.NotifyErrTimeout
	case errors.As(err, &opErr):
		return domain.NotifyErrConnect
	}
	return ""
}

// classOfText classifies by the words of an error (services that do not
// use HTTP, errors flattened to text by the service).
func classOfText(msg string) string {
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(msg, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("no such host", "server misbehaving", "lookup "):
		return domain.NotifyErrDNS
	case has("timed out", "timeout", "deadline exceeded"):
		return domain.NotifyErrTimeout
	case has("x509", "tls:", "certificate", "handshake", "starttls"):
		return domain.NotifyErrTLS
	case has("connection refused", "no route to host", "network is unreachable", "connection reset", "broken pipe", "eof"):
		return domain.NotifyErrConnect
	case has("535", "534", "530 ", "authentication", "unauthorized", "forbidden", "auth failed", "invalid token",
		"invalid credentials", "401", "403"):
		return domain.NotifyErrAuth
	}
	// Anything else (an SMTP server refusing a recipient, a chat API
	// answering "ok": false) is a refusal of the message.
	return domain.NotifyErrRejected
}

// Message is the sentence the UI shows for an error class: what happened
// and what to check. It never contains the address.
func Message(class string) string {
	switch class {
	case domain.NotifyErrDNS:
		return "The server name in the address could not be found. Check the address."
	case domain.NotifyErrConnect:
		return "Docker Manager could not connect to the service. Check the host and port and that the service is reachable from Docker Manager."
	case domain.NotifyErrTLS:
		return "The secure connection failed. Check the server's certificate and the encryption setting."
	case domain.NotifyErrTimeout:
		return "The service did not answer in time. Check that it is reachable from Docker Manager."
	case domain.NotifyErrAuth:
		return "The service refused the credentials. Check the token, password or webhook address."
	case domain.NotifyErrHTTP4xx:
		return "The service rejected the message. Check the address and the target (channel, topic, chat or recipient)."
	case domain.NotifyErrHTTP5xx:
		return "The service had a problem on its side. Try again later."
	case domain.NotifyErrRedirect:
		return "The service redirected to another address. Enter the final address instead."
	case domain.NotifyErrInvalidURL:
		return "The address can't be used for this service. Check it and save it again."
	case domain.NotifyErrRejected:
		return "The service did not accept the message. Check the channel's settings."
	}
	return ""
}
