// Package enroll exchanges a one-use enrollment token for the agent's
// credential (POST /agent/v1/enroll, docs/internal/protocol/agent-v1.md). The token
// travels only in the Authorization header over the validated transport
// (internal/agent/transport); it is never logged or put in a URL.
package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Error is a failed enrollment.
type Error struct {
	// Status is the HTTP status (0 for network failures).
	Status int
	// Code is the manager's stable error code (e.g. unauthenticated,
	// engine_already_enrolled, version_unsupported).
	Code    string
	Message string
	// RetryAfter is the manager's Retry-After hint (429, 503).
	RetryAfter time.Duration
	cause      error
}

func (e *Error) Error() string {
	switch {
	case e.Status == 0:
		return "cannot reach the manager: " + e.cause.Error()
	case e.Code != "":
		return fmt.Sprintf("enrollment refused (%d %s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("enrollment failed with HTTP %d", e.Status)
}

func (e *Error) Unwrap() error { return e.cause }

// Retryable reports whether the same token may succeed later (network
// failures, rate limits, manager errors). Refusals (invalid or used token,
// duplicate Engine, version outside the window, malformed request) are
// final for this token.
func (e *Error) Retryable() bool {
	switch {
	case e.Status == 0, e.Status == http.StatusTooManyRequests, e.Status == http.StatusRequestTimeout, e.Status >= 500:
		return true
	}
	return false
}

// maxResponse bounds the response body.
const maxResponse = 64 << 10

// Enroll posts the enrollment request with token to url (the manager
// origin plus protocol.EnrollPath) using client.
func Enroll(ctx context.Context, client *http.Client, url, token, userAgent string, req protocol.EnrollRequest) (protocol.EnrollResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.EnrollResponse{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return protocol.EnrollResponse{}, err
	}
	hreq.Header.Set("Authorization", "Bearer "+token)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	hreq.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(hreq)
	if err != nil {
		return protocol.EnrollResponse{}, &Error{cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return protocol.EnrollResponse{}, &Error{cause: err}
	}
	if resp.StatusCode != http.StatusCreated {
		e := &Error{Status: resp.StatusCode}
		var p struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &p) == nil {
			e.Code, e.Message = p.Code, p.Message
		}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
		return protocol.EnrollResponse{}, e
	}
	var out protocol.EnrollResponse
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("enroll: malformed response: %w", err)
	}
	if err := out.Validate(); err != nil {
		return out, errors.Join(errors.New("enroll: unexpected response"), err)
	}
	return out, nil
}
