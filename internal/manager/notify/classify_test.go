package notify

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
)

func TestClassifyPrefersWhatTheAdapterSaw(t *testing.T) {
	ctx := context.Background()
	send := errors.New("failed to send: https://discord.com/api/webhooks/1/secret-token")
	cases := map[string]struct {
		p    *probe
		want string
	}{
		"dns":       {&probe{netErr: &net.DNSError{Err: "no such host", Name: "hooks.example.com", IsNotFound: true}}, domain.NotifyErrDNS},
		"tls":       {&probe{netErr: fmt.Errorf("wrapped: %w", x509.UnknownAuthorityError{})}, domain.NotifyErrTLS},
		"connect":   {&probe{netErr: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}, domain.NotifyErrConnect},
		"timeout":   {&probe{netErr: fmt.Errorf("get: %w", context.DeadlineExceeded)}, domain.NotifyErrTimeout},
		"redirect":  {&probe{redirect: true, status: http.StatusFound}, domain.NotifyErrRedirect},
		"panic":     {&probe{panicked: true}, domain.NotifyErrRejected},
		"401":       {&probe{status: http.StatusUnauthorized}, domain.NotifyErrAuth},
		"403":       {&probe{status: http.StatusForbidden}, domain.NotifyErrAuth},
		"404":       {&probe{status: http.StatusNotFound}, domain.NotifyErrHTTP4xx},
		"429":       {&probe{status: http.StatusTooManyRequests}, domain.NotifyErrHTTP4xx},
		"503":       {&probe{status: http.StatusServiceUnavailable}, domain.NotifyErrHTTP5xx},
		"200 but":   {&probe{status: http.StatusOK}, domain.NotifyErrRejected},
		"no signal": {&probe{}, domain.NotifyErrRejected},
	}
	for name, c := range cases {
		if got := classify(ctx, send, c.p); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
	// The send's own deadline.
	dctx, cancel := context.WithDeadline(ctx, time.Unix(0, 0))
	defer cancel()
	if got := classify(dctx, context.DeadlineExceeded, &probe{}); got != domain.NotifyErrTimeout {
		t.Errorf("deadline: %s", got)
	}
}

func TestClassifyByTextForServicesWithoutHTTP(t *testing.T) {
	cases := map[string]string{
		"dial tcp: lookup mail.example.com: no such host": domain.NotifyErrDNS,
		"smtp: i/o timeout": domain.NotifyErrTimeout,
		"tls: failed to verify certificate: x509: unknown authority":  domain.NotifyErrTLS,
		"server does not support STARTTLS":                            domain.NotifyErrTLS,
		"dial tcp 10.0.0.1:25: connect: connection refused":           domain.NotifyErrConnect,
		"535 5.7.8 Username and Password not accepted":                domain.NotifyErrAuth,
		"authentication failed":                                       domain.NotifyErrAuth,
		"550 5.1.1 The email account that you tried to reach is gone": domain.NotifyErrRejected,
		"telegram: chat not found":                                    domain.NotifyErrRejected,
	}
	for msg, want := range cases {
		if got := classOfText(strings.ToLower(msg)); got != want {
			t.Errorf("%q: %s, want %s", msg, got, want)
		}
	}
}

func TestEveryClassHasASentenceWithoutAnAddress(t *testing.T) {
	for _, c := range domain.NotificationErrorClasses() {
		m := Message(c)
		if m == "" || !strings.HasSuffix(m, ".") || strings.Contains(m, "://") {
			t.Errorf("%s: %q", c, m)
		}
	}
	if Message("ok") != "" || Message("") != "" {
		t.Error("success has no message")
	}
}

func TestValidateMakesNoRequests(t *testing.T) {
	cases := map[string]string{
		"discord://token@123456789012345678":                                                         "discord",
		"slack://hook:WNA3PBYV6-F20DUQND3RQ-Webc4MAvoacrpPakR8phF0zi@webhook":                        "slack",
		"telegram://123456:ABCdefGHIjklMNOpqrSTUvwxYZ@telegram?chats=@ops":                           "telegram",
		"smtp://user:pass@mail.example.com:587/?fromaddress=a@example.com&toaddresses=b@example.com": "smtp",
		"ntfy://ntfy.sh/topic":                        "ntfy",
		"gotify://gotify.example.com/AzyoeNS.D4iJLVa": "gotify",
		"generic+https://hooks.example.com/incoming":  "generic",
		// Matrix signs in while it initializes: the attempt counts as valid.
		"matrix://user:password@matrix.example.com/?rooms=!room:example.com": "matrix",
	}
	for addr, want := range cases {
		got, err := validate(addr, time.Second)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %s", want, got, err, want)
		}
	}
	for _, bad := range []string{"", "no scheme", "carrierpigeon://coop", "https://hooks.example.com", "slack://nope@webhook",
		"discord://token@x\n", strings.Repeat("a", MaxAddressLen+1)} {
		if _, err := validate(bad, time.Second); err == nil {
			t.Errorf("%.40q was accepted", bad)
		}
	}
}

func TestTargetIsOnlyANonSecretHost(t *testing.T) {
	cases := map[string]string{
		"smtp://user:pass@Mail.Example.com:587/?fromaddress=a@example.com&toaddresses=b@example.com": "mail.example.com",
		"ntfy://ntfy.example.com/secret-topic":                                                       "ntfy.example.com",
		"gotify://push.example.com/AzyoeNS.D4iJLVa":                                                  "push.example.com",
		"generic+https://hooks.example.com/incoming/secret":                                          "hooks.example.com",
		"matrix://user:password@matrix.example.com/?rooms=!r:example":                                "matrix.example.com",
		// The host holds a webhook ID, channel, bot or user key.
		"discord://token@123456789012345678":                                  "",
		"slack://hook:WNA3PBYV6-F20DUQND3RQ-Webc4MAvoacrpPakR8phF0zi@webhook": "",
		"telegram://123456:ABC@telegram?chats=@ops":                           "",
		"pushover://shoutrrr:token@userkey/":                                  "",
		"teams://?host=https%3A%2F%2Fprod.example.com%2Fworkflows":            "",
		"not a url": "",
	}
	for addr, want := range cases {
		if got := targetOf(addr); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
}
