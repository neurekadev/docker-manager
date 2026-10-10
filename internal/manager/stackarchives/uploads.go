package stackarchives

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
)

// Upload is an uploaded archive waiting to become a stack.
type Upload struct {
	ID string `json:"id"`
	// OwnerUserID is the user who uploaded it (also through an API token):
	// only they see and use it.
	OwnerUserID string    `json:"ownerUserId"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Manifest    Manifest  `json:"manifest"`
	// Project and Volumes are the parts as read from the archive.
	Project PartStats            `json:"project"`
	Volumes map[string]PartStats `json:"volumes"`
	// PinnedName is the project name a root Compose file pins with a
	// top-level name: ("" none).
	PinnedName string `json:"pinnedName,omitempty"`
}

// Upload errors.
var (
	ErrUploadNotFound   = errors.New("stack archive upload not found")
	ErrUploadTooLarge   = errors.New("the archive exceeds the size limit")
	ErrTooManyUploads   = errors.New("too many uploaded archives")
	ErrNoSpace          = errors.New("not enough free space on the manager")
	ErrUploadIncomplete = errors.New("the upload ended before Content-Length bytes")
	ErrUploadInUse      = errors.New("a stack is being created from this archive")
)

func (s *Service) uploadPath(id string) string {
	return filepath.Join(s.uploadsDir(), id+FileExtension)
}
func (s *Service) sidecarPath(id string) string {
	return filepath.Join(s.uploadsDir(), id+".json")
}

// loadUploads reads the uploads of earlier runs back (their sidecars).
func (s *Service) loadUploads() error {
	entries, err := os.ReadDir(s.uploadsDir())
	if err != nil {
		return fmt.Errorf("stackarchives: %w", err)
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.uploadsDir(), e.Name()))
		var u Upload
		if err == nil {
			err = json.Unmarshal(b, &u)
		}
		if _, serr := os.Stat(s.uploadPath(id)); err != nil || serr != nil || u.ID != id {
			s.removeUpload(id)
			continue
		}
		s.uploads[id] = &u
	}
	// Archives without a sidecar are leftovers.
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), FileExtension); ok && s.uploads[id] == nil {
			_ = os.Remove(filepath.Join(s.uploadsDir(), e.Name()))
		}
	}
	return nil
}

func (s *Service) removeUpload(id string) {
	_ = os.Remove(s.uploadPath(id))
	_ = os.Remove(s.uploadPath(id) + partSuffix)
	_ = os.Remove(s.sidecarPath(id))
	s.mu.Lock()
	delete(s.uploads, id)
	s.mu.Unlock()
}

// StoreUpload streams an uploaded archive of size bytes to the data
// directory, validating it on the way (manifest, parts, member names and
// types). Nothing reaches an agent yet.
func (s *Service) StoreUpload(ctx context.Context, p authz.Principal, size int64, body io.Reader) (Upload, error) {
	if p.UserID == "" {
		return Upload{}, ErrUploadNotFound
	}
	if size > s.opts.MaxSize {
		return Upload{}, fmt.Errorf("%w (%s)", ErrUploadTooLarge, humanize.Bytes(s.opts.MaxSize))
	}
	id := ids.New()
	// Uploads in progress count toward the user's limit, and their size is
	// claimed of the free space until they end. At the limit, the user's
	// oldest upload no stack is being created from makes room (an upload
	// abandoned in a closed tab must not lock its owner out for a day).
	s.mu.Lock()
	n, oldest := 0, ""
	for uid, u := range s.uploads {
		if u.OwnerUserID != p.UserID {
			continue
		}
		n++
		if _, used := s.inUse[uid]; !used && (oldest == "" || u.CreatedAt.Before(s.uploads[oldest].CreatedAt) ||
			u.CreatedAt.Equal(s.uploads[oldest].CreatedAt) && uid < oldest) {
			oldest = uid
		}
	}
	for _, owner := range s.pending {
		if owner == p.UserID {
			n++
		}
	}
	if n >= MaxUploadsPerUser && oldest == "" {
		s.mu.Unlock()
		return Upload{}, ErrTooManyUploads
	}
	evict := n >= MaxUploadsPerUser
	if evict {
		delete(s.uploads, oldest)
	}
	s.pending[id] = p.UserID
	s.mu.Unlock()
	if evict {
		s.removeUpload(oldest)
		s.log.Info("discarded the oldest stack archive upload to make room", "archive_id", oldest)
	}
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()
	release, err := s.reserve(s.uploadsDir(), size)
	if err != nil {
		return Upload{}, err
	}
	defer release()
	part := s.uploadPath(id) + partSuffix
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Upload{}, err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(part)
		}
	}()
	pr, pw := io.Pipe()
	type result struct {
		u   Upload
		err error
	}
	done := make(chan result, 1)
	go func() {
		u, err := inspect(pr, max(size*maxRatio, s.opts.MaxSize))
		if err != nil {
			_ = pr.CloseWithError(err)
		} else {
			_, _ = io.Copy(io.Discard, pr) // gzip trailer, tar padding
		}
		done <- result{u, err}
	}()
	h := sha256.New()
	src := &readErr{r: io.LimitReader(body, size)}
	dst := &writeErr{w: f}
	written, cerr := io.Copy(io.MultiWriter(dst, h, pw), src)
	// Short: the body ended early (a write error stopped the copy
	// otherwise: the disk, or the validator refusing the archive).
	short := src.err != nil || (cerr == nil && written != size)
	if dst.err != nil {
		_ = pw.CloseWithError(dst.err)
		<-done
		return Upload{}, dst.err
	}
	if short {
		// The client went away: the validator must not take the cut-off
		// archive for a complete one.
		_ = pw.CloseWithError(ErrUploadIncomplete)
	} else {
		_ = pw.Close()
	}
	res := <-done
	switch {
	case short && ctx.Err() != nil:
		return Upload{}, ctx.Err()
	case short:
		return Upload{}, ErrUploadIncomplete
	case res.err != nil:
		return Upload{}, res.err
	}
	if err := f.Sync(); err != nil {
		return Upload{}, err
	}
	if err := f.Close(); err != nil {
		return Upload{}, err
	}
	u := res.u
	now := s.now()
	u.ID, u.OwnerUserID, u.Size, u.SHA256, u.CreatedAt, u.ExpiresAt = id, p.UserID, size, hex.EncodeToString(h.Sum(nil)), now, now.Add(s.opts.Retention)
	b, err := json.Marshal(u)
	if err != nil {
		return Upload{}, err
	}
	if err := os.WriteFile(s.sidecarPath(id), b, 0o600); err != nil {
		return Upload{}, err
	}
	if err := os.Rename(part, s.uploadPath(id)); err != nil {
		_ = os.Remove(s.sidecarPath(id))
		return Upload{}, err
	}
	ok = true
	s.mu.Lock()
	s.uploads[id] = &u
	s.mu.Unlock()
	s.log.Info("stack archive uploaded", "archive_id", id, "bytes", size, "volumes", len(u.Manifest.Volumes))
	return u, nil
}

// maxRatio bounds what an upload unpacks to: this many times its size
// (at least the archive limit), against decompression bombs.
const maxRatio = 100

// readErr remembers the error of the request body (a client that went
// away), apart from errors writing the copy.
type readErr struct {
	r   io.Reader
	err error
}

func (e *readErr) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		e.err = err
	}
	return n, err
}

// writeErr remembers the error of writing the upload's file.
type writeErr struct {
	w   io.Writer
	err error
}

func (e *writeErr) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil {
		e.err = err
	}
	return n, err
}

// inspect reads a whole archive and measures its parts; limit bounds the
// bytes of their files.
func inspect(r io.Reader, limit int64) (Upload, error) {
	rd, err := NewReader(r)
	if err != nil {
		return Upload{}, err
	}
	rd.Limit = limit
	u := Upload{Manifest: rd.Manifest(), Volumes: map[string]PartStats{}}
	for {
		p, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Upload{}, err
		}
		st, err := rd.WriteTo(p, io.Discard)
		if err != nil {
			return Upload{}, invalid(err)
		}
		if p.Volume == "" {
			u.Project = st
		} else {
			u.Volumes[p.Volume] = st
		}
	}
	if err := rd.Close(); err != nil {
		return Upload{}, err
	}
	u.PinnedName = PinnedName(rd.ComposeFiles())
	return u, nil
}

// Upload returns p's upload.
func (s *Service) Upload(p authz.Principal, id string) (Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok || u.OwnerUserID == "" || u.OwnerUserID != p.UserID || !s.clk.Now().Before(u.ExpiresAt) {
		return Upload{}, ErrUploadNotFound
	}
	return *u, nil
}

// Uploads lists p's uploads (newest first).
func (s *Service) Uploads(p authz.Principal) []Upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Upload
	now := s.clk.Now()
	for _, u := range s.uploads {
		if u.OwnerUserID == p.UserID && now.Before(u.ExpiresAt) {
			out = append(out, *u)
		}
	}
	slices.SortFunc(out, func(a, b Upload) int { return strings.Compare(b.ID, a.ID) })
	return out
}

// DeleteUpload discards p's upload (not while a stack is created from it:
// the check and the removal from the list are one step).
func (s *Service) DeleteUpload(p authz.Principal, id string) error {
	if _, err := s.Upload(p, id); err != nil {
		return err
	}
	s.mu.Lock()
	if _, used := s.inUse[id]; used {
		s.mu.Unlock()
		return ErrUploadInUse
	}
	delete(s.uploads, id)
	s.mu.Unlock()
	s.removeUpload(id)
	return nil
}

// openUpload opens an upload's archive for reading.
func (s *Service) openUpload(id string) (*Reader, func(), error) {
	f, err := os.Open(s.uploadPath(id))
	if err != nil {
		return nil, nil, err
	}
	rd, err := NewReader(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return rd, func() { _ = f.Close() }, nil
}
