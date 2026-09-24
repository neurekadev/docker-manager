package fscorpus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Swapper swaps a directory for a symlink (to a directory outside the
// root) and back, to reproduce time-of-check/time-of-use races: a consumer
// that validates dir and then opens dir/file may follow the symlink.
type Swapper struct {
	dir, target, parked string

	mu      sync.Mutex
	swapped bool
	flips   atomic.Int64
}

// NewSwapper prepares to swap dir (an existing directory) for a symlink to
// target.
func NewSwapper(dir, target string) (*Swapper, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("fscorpus: %s is not a directory", dir)
	}
	return &Swapper{dir: dir, target: target, parked: dir + ".toctou-parked"}, nil
}

// Swap moves the directory aside and puts the symlink in its place.
func (s *Swapper) Swap() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.swapped {
		return nil
	}
	if err := os.Rename(s.dir, s.parked); err != nil {
		return err
	}
	if err := os.Symlink(s.target, s.dir); err != nil {
		return errors.Join(err, os.Rename(s.parked, s.dir))
	}
	s.swapped = true
	s.flips.Add(1)
	return nil
}

// Restore removes the symlink and puts the directory back.
func (s *Swapper) Restore() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.swapped {
		return nil
	}
	if err := os.Remove(s.dir); err != nil {
		return err
	}
	if err := os.Rename(s.parked, s.dir); err != nil {
		return err
	}
	s.swapped = false
	s.flips.Add(1)
	return nil
}

// Swapped reports whether the symlink is currently in place.
func (s *Swapper) Swapped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.swapped
}

// Flips returns how many swaps and restores happened.
func (s *Swapper) Flips() int64 { return s.flips.Load() }

// Run flips until ctx is done, then restores the directory. hold is how
// long each state (directory / symlink) is kept before flipping again; a
// small non-zero hold lets a concurrent consumer observe both states.
func (s *Swapper) Run(ctx context.Context, hold time.Duration) error {
	pause := func() {
		if hold > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(hold):
			}
		}
	}
	for ctx.Err() == nil {
		if err := s.Swap(); err != nil {
			return errors.Join(err, s.Restore())
		}
		pause()
		if err := s.Restore(); err != nil {
			return err
		}
		pause()
	}
	return s.Restore()
}

// RaceOptions bounds RaceWhile.
type RaceOptions struct {
	// The race continues until op ran MinAttempts times, succeeded
	// MinSuccesses times and the swapper flipped MinFlips times, so it is
	// never vacuous (a consumer that always fails is not "safe").
	MinAttempts  int
	MinSuccesses int
	MinFlips     int64
	// Timeout fails the race if the minimums are not reached (default 1m).
	Timeout time.Duration
	// Hold is how long each state is kept (default 200 microseconds).
	Hold time.Duration
}

// RaceResult summarizes RaceWhile.
type RaceResult struct {
	Attempts  int
	Successes int
	// Errors counts op failures (expected: a safe consumer refuses paths
	// that became symlinks).
	Errors int
	Flips  int64
}

// RaceWhile runs op repeatedly while a Swapper flips dir to a symlink to
// target, until the minimums in opts are reached. It returns the first
// error op wraps with ErrEscaped, a timeout error, or the swapper's own
// error. The directory is restored before returning.
func RaceWhile(dir, target string, opts RaceOptions, op func() error) (RaceResult, error) {
	if opts.Timeout == 0 {
		opts.Timeout = time.Minute
	}
	if opts.Hold == 0 {
		opts.Hold = 200 * time.Microsecond
	}
	s, err := NewSwapper(dir, target)
	if err != nil {
		return RaceResult{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, opts.Hold) }()

	var res RaceResult
	deadline := time.Now().Add(opts.Timeout)
	var escaped error
	for res.Attempts < opts.MinAttempts || res.Successes < opts.MinSuccesses || s.Flips() < opts.MinFlips {
		if time.Now().After(deadline) {
			cancel()
			<-done
			res.Flips = s.Flips()
			return res, fmt.Errorf("fscorpus: race did not reach its minimums within %s: %+v", opts.Timeout, res)
		}
		res.Attempts++
		if err := op(); err != nil {
			if errors.Is(err, ErrEscaped) {
				escaped = err
				break
			}
			res.Errors++
		} else {
			res.Successes++
		}
	}
	cancel()
	runErr := <-done
	res.Flips = s.Flips()
	if escaped != nil {
		return res, escaped
	}
	return res, runErr
}

// ErrEscaped marks an operation that observed data from outside the root.
// Race operations wrap it so RaceWhile stops at the first escape.
var ErrEscaped = errors.New("fscorpus: escaped the root")
