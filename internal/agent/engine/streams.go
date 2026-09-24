package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
)

// Events streams Engine events to fn until ctx ends, the Engine closes the
// stream or fn returns an error (which is returned). A clean end of stream
// returns nil; callers resubscribe with Since set to the last event time.
func (c *Client) Events(ctx context.Context, f EventFilter, fn func(Event) error) error {
	const op = "events.stream"
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // ends the SDK goroutine and the HTTP stream
	filters := client.Filters{}
	if len(f.Types) > 0 {
		filters.Add("type", f.Types...)
	}
	if len(f.Labels) > 0 {
		filters.Add("label", f.Labels...)
	}
	opts := client.EventsListOptions{Filters: filters}
	if !f.Since.IsZero() {
		opts.Since = strconv.FormatInt(f.Since.Unix(), 10) + "." + strconv.Itoa(f.Since.Nanosecond())
	}
	res := c.api.Events(ctx, opts)
	for {
		select {
		case m := <-res.Messages:
			if err := fn(eventFrom(m)); err != nil {
				return err
			}
		case err := <-res.Err:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return wrap(op, err)
		case <-ctx.Done():
			return wrap(op, ctx.Err())
		}
	}
}

func eventFrom(m events.Message) Event {
	t := time.Unix(0, m.TimeNano).UTC()
	if m.TimeNano == 0 {
		t = time.Unix(m.Time, 0).UTC()
	}
	return Event{
		Type: string(m.Type), Action: string(m.Action), ActorID: m.Actor.ID,
		Attributes: m.Actor.Attributes, Scope: m.Scope, Time: t,
	}
}

// maxLogChunk bounds one LogEntry; longer lines are split.
const maxLogChunk = 64 << 10

// Logs streams a container's output to fn. With o.Follow it runs until ctx
// ends, the container stops or fn returns an error; otherwise until the
// existing output is consumed.
func (c *Client) Logs(ctx context.Context, id string, o LogOptions, fn func(LogEntry) error) error {
	const op = "logs.stream"
	d, err := c.InspectContainer(ctx, id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !o.Stdout && !o.Stderr {
		o.Stdout, o.Stderr = true, true
	}
	opts := client.ContainerLogsOptions{
		ShowStdout: o.Stdout, ShowStderr: o.Stderr, Follow: o.Follow, Timestamps: o.Timestamps, Tail: "all",
	}
	if !o.TailAll {
		opts.Tail = strconv.Itoa(max(o.Tail, 0))
		if o.Tail == 0 {
			opts.Tail = "all"
		}
	}
	if !o.Since.IsZero() {
		opts.Since = strconv.FormatInt(o.Since.Unix(), 10)
	}
	if !o.Until.IsZero() {
		opts.Until = strconv.FormatInt(o.Until.Unix(), 10)
	}
	rc, err := c.api.ContainerLogs(ctx, d.ID, opts)
	if err != nil {
		return wrap(op, err)
	}
	defer rc.Close()
	err = demuxLogs(rc, d.Tty, o.Timestamps, fn)
	var stop *stopError
	switch {
	case errors.As(err, &stop):
		return stop.err
	case err != nil && ctx.Err() != nil:
		return wrap(op, ctx.Err())
	}
	return wrap(op, err)
}

// demuxLogs splits a (multiplexed unless tty) log stream into entries.
func demuxLogs(r io.Reader, tty, timestamps bool, fn func(LogEntry) error) error {
	out := &lineWriter{stream: Stdout, timestamps: timestamps, fn: fn}
	errw := &lineWriter{stream: Stderr, timestamps: timestamps, fn: fn}
	var err error
	if tty {
		_, err = io.Copy(out, r)
	} else {
		_, err = stdcopy.StdCopy(out, errw, r)
	}
	if err != nil {
		return err // incomplete trailing data is dropped with the error
	}
	return errors.Join(out.flush(), errw.flush())
}

type stopError struct{ err error }

func (e *stopError) Error() string { return e.err.Error() }

// lineWriter turns writes into LogEntry values, one per line (bounded by
// maxLogChunk), parsing the Engine's RFC 3339 timestamp prefix.
type lineWriter struct {
	stream     LogStream
	timestamps bool
	fn         func(LogEntry) error
	buf        []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			if len(w.buf) >= maxLogChunk {
				if err := w.emit(w.buf[:maxLogChunk]); err != nil {
					return 0, err
				}
				w.buf = append(w.buf[:0], w.buf[maxLogChunk:]...)
				continue
			}
			return len(p), nil
		}
		if err := w.emit(w.buf[:i+1]); err != nil {
			return 0, err
		}
		w.buf = append(w.buf[:0], w.buf[i+1:]...)
	}
}

func (w *lineWriter) flush() error {
	if len(w.buf) == 0 {
		return nil
	}
	err := w.emit(w.buf)
	w.buf = w.buf[:0]
	return err
}

func (w *lineWriter) emit(line []byte) error {
	e := LogEntry{Stream: w.stream}
	if w.timestamps {
		if sp := bytes.IndexByte(line, ' '); sp > 0 {
			if t, err := time.Parse(time.RFC3339Nano, string(line[:sp])); err == nil {
				e.Time = t.UTC()
				line = line[sp+1:]
			}
		}
	}
	e.Data = bytes.Clone(line)
	if err := w.fn(e); err != nil {
		return &stopError{err: err}
	}
	return nil
}

// Stats streams resource usage samples of a container to fn (a single
// sample unless stream is set). CPU percent needs two samples: with stream
// the first sample reports 0; without stream the Engine collects a prior
// sample itself.
func (c *Client) Stats(ctx context.Context, id string, stream bool, fn func(Stats) error) error {
	const op = "stats.stream"
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	res, err := c.api.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: stream, IncludePreviousSample: !stream})
	if err != nil {
		return wrap(op, err)
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)
	for {
		var s container.StatsResponse
		if err := dec.Decode(&s); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if ctx.Err() != nil {
				return wrap(op, ctx.Err())
			}
			return wrap(op, err)
		}
		if err := fn(statsFrom(&s)); err != nil {
			return err
		}
		if !stream {
			return nil
		}
	}
}

// statsFrom computes `docker stats`-style values from a raw sample.
func statsFrom(s *container.StatsResponse) Stats {
	out := Stats{Read: s.Read.UTC(), PIDs: s.PidsStats.Current, MemoryLimit: s.MemoryStats.Limit}
	online := s.CPUStats.OnlineCPUs
	if online == 0 {
		online = uint32(len(s.CPUStats.CPUUsage.PercpuUsage)) //nolint:gosec // G115: a CPU count
	}
	out.OnlineCPUs = online
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if s.PreCPUStats.CPUUsage.TotalUsage > 0 && cpuDelta > 0 && sysDelta > 0 {
		out.CPUPercent = cpuDelta / sysDelta * float64(online) * 100
	}
	usage := s.MemoryStats.Usage
	// Page cache is excluded like the Docker CLI does: cgroup v2 reports
	// inactive_file, cgroup v1 total_inactive_file.
	for _, k := range []string{"inactive_file", "total_inactive_file"} {
		if v, ok := s.MemoryStats.Stats[k]; ok && v < usage {
			usage -= v
			break
		}
	}
	out.MemoryUsage = usage
	if out.MemoryLimit > 0 {
		out.MemoryPercent = float64(usage) / float64(out.MemoryLimit) * 100
	}
	for _, n := range s.Networks {
		out.NetworkRx += n.RxBytes
		out.NetworkTx += n.TxBytes
	}
	for _, b := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(b.Op) {
		case "read":
			out.BlockRead += b.Value
		case "write":
			out.BlockWrite += b.Value
		}
	}
	return out
}

// consumeJSONStream reads a JSON message stream to the end and returns the
// first error message it carries.
func consumeJSONStream(ctx context.Context, op string, r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var m jsonstream.Message
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if ctx.Err() != nil {
				return wrap(op, ctx.Err())
			}
			return wrap(op, err)
		}
		if m.Error != nil {
			return newError(op, pullErrorCode(m.Error.Code, m.Error.Message), "%s", m.Error.Message)
		}
	}
}
