// Package buildrun is shared by the agent's image build executors (#33):
// manual Git builds (image.build, internal/agent/builds) and Compose
// build sections (stack.build and the deploy's build step,
// internal/agent/stacks). It turns BuildKit progress into bounded job
// progress scrubbed of the attempt's credentials, and runs a build under
// cancellation and a timeout.
package buildrun

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Defaults.
const (
	// DefaultCancelPoll is how often a running build checks for cancellation.
	DefaultCancelPoll = time.Second
	// maxMessage bounds one progress message.
	maxMessage = 4 << 10
	// logFlushBytes flushes buffered build output as one progress message.
	logFlushBytes = 2 << 10
	// maxLogMessages bounds the build output messages of one attempt (the
	// job keeps a bounded event log, #26).
	maxLogMessages = 300
)

// Progress turns BuildKit events into job progress: step status changes
// are reported at once; build output is buffered per step and flushed in
// bounded chunks; everything is scrubbed of the attempt's secrets.
type Progress struct {
	ctx      context.Context
	sc       *jobexec.StepContext
	mu       sync.Mutex
	prefix   string
	step     string
	buf      strings.Builder
	logs     int
	dropped  bool
	scrubber func(string) string
}

// NewProgress reports to sc.
func NewProgress(ctx context.Context, sc *jobexec.StepContext) *Progress {
	return &Progress{ctx: ctx, sc: sc, scrubber: Scrubber(sc.Secrets)}
}

// SetPrefix flushes buffered output and prefixes later messages (e.g. the
// image a Compose service builds).
func (p *Progress) SetPrefix(prefix string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushLocked()
	p.prefix = prefix
	p.step = ""
}

// Send reports one message (scrubbed and bounded).
func (p *Progress) Send(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sendLocked(msg)
}

func (p *Progress) sendLocked(msg string) {
	if p.prefix != "" {
		msg = p.prefix + ": " + msg
	}
	msg = p.scrubber(msg)
	if len(msg) > maxMessage {
		msg = msg[:maxMessage] + " …"
	}
	p.sc.Progress(p.ctx, -1, msg)
}

func (p *Progress) flushLocked() {
	if p.buf.Len() == 0 {
		return
	}
	text := p.buf.String()
	p.buf.Reset()
	if p.logs >= maxLogMessages {
		if !p.dropped {
			p.dropped = true
			p.sendLocked("build output truncated (the job keeps a bounded log)")
		}
		return
	}
	p.logs++
	p.sendLocked(p.step + "\n" + strings.TrimRight(text, "\n"))
}

// Flush reports buffered build output.
func (p *Progress) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushLocked()
}

// Event is an engine.BuildSpec.Progress callback.
func (p *Progress) Event(ev engine.BuildEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ev.Status == "log" {
		if ev.Step != p.step {
			p.flushLocked()
			p.step = ev.Step
		}
		p.buf.Write(ev.Log)
		if p.buf.Len() >= logFlushBytes {
			p.flushLocked()
		}
		return
	}
	p.flushLocked()
	msg := ev.Step + ": " + ev.Status
	if ev.Error != "" {
		msg += ": " + ev.Error
	}
	p.sendLocked(msg)
}

// Scrubber replaces every secret of the attempt (and its common encodings)
// in text.
func Scrubber(s *protocol.CommandSecrets) func(string) string {
	var olds []string
	add := func(user, secret string) {
		if len(secret) < 4 {
			return
		}
		olds = append(olds, secret,
			base64.StdEncoding.EncodeToString([]byte(user+":"+secret)),
			base64.StdEncoding.EncodeToString([]byte(secret)),
			base64.URLEncoding.EncodeToString([]byte(secret)))
	}
	if s != nil {
		for _, g := range s.Git {
			add(g.Username, g.Secret)
		}
		for _, r := range s.Registries {
			add(r.Username, r.Secret)
		}
	}
	if len(olds) == 0 {
		return func(t string) string { return t }
	}
	pairs := make([]string, 0, 2*len(olds))
	for _, o := range olds {
		pairs = append(pairs, o, "[redacted]")
	}
	r := strings.NewReplacer(pairs...)
	return r.Replace
}

// ScrubError returns err with the attempt's secrets removed from its text.
// An Engine error keeps its code (so stack jobs still classify it); the
// original error, which holds the secret, is dropped.
func ScrubError(s *protocol.CommandSecrets, err error) error {
	if err == nil {
		return nil
	}
	clean := Scrubber(s)(err.Error())
	if clean == err.Error() {
		return err
	}
	var ee *engine.Error
	if errors.As(err, &ee) {
		return &engine.Error{Code: ee.Code, Message: clean}
	}
	return errors.New(clean)
}

// Options configures Run.
type Options struct {
	Clock clock.Clock
	// Poll is how often cancellation is checked (default DefaultCancelPoll).
	Poll time.Duration
	// Timeout stops the build with a failure (required).
	Timeout time.Duration
}

// Run runs build with a context that ends when cancellation is requested
// (sc.CancelRequested, polled) or the timeout passes. It returns ctx's
// error when ctx ended (agent shutdown: the attempt is left to recovery),
// an error wrapping jobexec.ErrStepCancelled after a cancellation (the job
// ends cancelled), a timeout failure, or build's own error (not scrubbed).
func Run(ctx context.Context, sc *jobexec.StepContext, o Options, build func(ctx context.Context) error) error {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Poll <= 0 {
		o.Poll = DefaultCancelPoll
	}
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var cancelled, timedOut bool
	var flagMu sync.Mutex
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		poll := o.Clock.NewTicker(o.Poll)
		defer poll.Stop()
		deadline := o.Clock.NewTimer(o.Timeout)
		defer deadline.Stop()
		for {
			select {
			case <-bctx.Done():
				return
			case <-deadline.C():
				flagMu.Lock()
				timedOut = true
				flagMu.Unlock()
				cancel()
				return
			case <-poll.C():
				if sc.CancelRequested() {
					flagMu.Lock()
					cancelled = true
					flagMu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	berr := build(bctx)
	cancel()
	<-watchDone
	flagMu.Lock()
	wasCancelled, wasTimedOut := cancelled, timedOut
	flagMu.Unlock()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case wasCancelled:
		return fmt.Errorf("the build was stopped: %w", jobexec.ErrStepCancelled)
	case wasTimedOut:
		return fmt.Errorf("the build did not finish within %s and was stopped", o.Timeout)
	}
	return berr
}
