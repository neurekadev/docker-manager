package fsroot

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/jobexec"
)

// progressEvery is how often a long file job reports where it is (each
// report updates the job record on the manager).
const progressEvery = 500 * time.Millisecond

// maxProgressName bounds the member name of a progress message (its end:
// the file name is what matters).
const maxProgressName = 160

// jobProgress reports where an archive or extraction job is: the percent
// of its bytes done and "12 of 340 · config/app.yml" (the member being
// worked on), at most every progressEvery. Names only, never contents.
// The methods do nothing on a nil *jobProgress (downloads, previews).
type jobProgress struct {
	send  func(percent int, message string)
	now   func() time.Time
	last  time.Time
	n     int   // the member being worked on (1-based)
	total int   // members in all (0 when unknown)
	done  int64 // bytes done
	size  int64 // bytes in all (0 when unknown)
	name  string
}

func (s *Service) newProgress(ctx context.Context, sc *jobexec.StepContext) *jobProgress {
	return &jobProgress{
		send: func(percent int, message string) { sc.Progress(ctx, percent, message) },
		now:  s.opts.Clock.Now,
	}
}

// next starts the next member.
func (p *jobProgress) next(name string) {
	if p == nil {
		return
	}
	p.n++
	p.name = name
	p.report()
}

// add counts n more bytes done.
func (p *jobProgress) add(n int) {
	if p == nil || n <= 0 {
		return
	}
	p.done += int64(n)
	p.report()
}

// Write counts the bytes written through it as done (behind an
// io.TeeReader).
func (p *jobProgress) Write(b []byte) (int, error) {
	p.add(len(b))
	return len(b), nil
}

func (p *jobProgress) report() {
	if p.n == 0 {
		return // bytes read before the first member (the format, a zip's directory)
	}
	now := p.now()
	if !p.last.IsZero() && now.Sub(p.last) < progressEvery {
		return
	}
	p.last = now
	percent := 0
	switch {
	case p.size > 0:
		percent = int(p.done * 100 / p.size)
	case p.total > 0:
		percent = (p.n - 1) * 100 / p.total
	}
	msg := progressName(p.name)
	if p.total > 0 {
		msg = fmt.Sprintf("%d of %d · %s", min(p.n, p.total), p.total, msg)
	}
	// 100 only when the job ends.
	p.send(min(max(percent, 0), 99), msg)
}

// progressName is a member name fit for a progress message: control
// characters replaced, long names shortened from the front.
func progressName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
	if len(s) > maxProgressName {
		s = "…" + s[len(s)-maxProgressName:]
	}
	return strings.ToValidUTF8(s, "")
}

// archiveFile is an archive being read.
type archiveFile interface {
	io.Reader
	io.ReaderAt
}

// readTally counts the bytes read from an archive file as done.
type readTally struct {
	f archiveFile
	p *jobProgress
}

func (t readTally) Read(b []byte) (int, error) {
	n, err := t.f.Read(b)
	t.p.add(n)
	return n, err
}

func (t readTally) ReadAt(b []byte, off int64) (int, error) {
	n, err := t.f.ReadAt(b, off)
	t.p.add(n)
	return n, err
}

// errCountBudget ends a member count that decompressed its budget.
var errCountBudget = errors.New("fsroot: member count budget spent")

// countReader bounds the decompressed bytes of a member count and ends
// it when ctx does (a skipped member is read through, however large).
type countReader struct {
	ctx  context.Context
	r    io.Reader
	left int64
}

func (c *countReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if c.left <= 0 {
		return 0, errCountBudget
	}
	if int64(len(b)) > c.left {
		b = b[:c.left]
	}
	n, err := c.r.Read(b)
	c.left -= int64(n)
	return n, err
}

// countTarMembers counts the members of a tar.gz archive in a pass that
// writes nothing, decompressing at most budget bytes (the extraction's own
// limit, so a bomb costs no more here than there). Best effort, for the
// progress: 0 (unknown) past limit members, past the budget, on
// cancellation and on damage, which the extraction itself reports.
func countTarMembers(ctx context.Context, f io.ReaderAt, size int64, limit int, budget int64) int {
	gz, err := gzip.NewReader(bufio.NewReader(io.NewSectionReader(f, 0, size)))
	if err != nil {
		return 0
	}
	gz.Multistream(false)
	tr := tar.NewReader(&countReader{ctx: ctx, r: gz, left: budget})
	n := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return n
		}
		if err != nil {
			return 0
		}
		if h.Typeflag != tar.TypeXGlobalHeader {
			if n++; n > limit {
				return 0
			}
		}
	}
}

// archiveSize counts the members an archive of paths gets and the bytes
// of its regular files (best effort, for the progress). skip is excluded
// as in writeArchive.
func (s *Service) archiveSize(ctx context.Context, r *scopeRoot, paths []string, skip string) (int, int64) {
	n, size := 0, int64(0)
	for _, p := range paths {
		_ = walk(ctx, r, p, true, s.limits.MaxWalk, func(rel string, fi fs.FileInfo) error {
			if rel == skip || rel == "." {
				return nil
			}
			n++
			if fi.Mode().IsRegular() {
				size += fi.Size()
			}
			return nil
		})
	}
	return n, size
}
