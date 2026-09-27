// Package logging configures Docker Manager's structured log/slog output and carries
// request-scoped loggers through contexts.
//
// Rules (see docs/internal/conventions/logging-and-security.md): never log secrets, tokens, credentials, passwords,
// file contents, Compose/.env values or raw query strings. Wrap values that
// might be sensitive in Secret so they render as "[REDACTED]".
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Format names accepted by DOCKER_MANAGER_LOG_FORMAT and DOCKER_AGENT_LOG_FORMAT.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// ParseLevel parses DOCKER_MANAGER_LOG_LEVEL and DOCKER_AGENT_LOG_LEVEL values: debug, info, warn, error.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
	}
}

// ParseFormat parses DOCKER_MANAGER_LOG_FORMAT and DOCKER_AGENT_LOG_FORMAT values: json (default) or text.
func ParseFormat(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", FormatJSON:
		return FormatJSON, nil
	case FormatText:
		return FormatText, nil
	default:
		return "", fmt.Errorf("unknown log format %q (want json or text)", s)
	}
}

// New builds a logger writing to w.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if format == FormatText {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

// Discard returns a logger that drops everything.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

type loggerKey struct{}

// IntoContext stores l in ctx.
func IntoContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// FromContext returns the logger stored in ctx, or slog.Default().
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

type requestIDKey struct{}

// WithRequestID stores the request ID in ctx.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID stored in ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// Secret wraps a sensitive value so it never reaches log output.
type Secret string

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// String implements fmt.Stringer so %v/%s also redact.
func (Secret) String() string { return "[REDACTED]" }
