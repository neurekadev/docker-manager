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
	s.mu.Lock()
	n := 0
	for _, u := range s.uploads {
		if u.OwnerUserID == p.UserID {
			n++
		}
	}
	s.mu.Unlock()
	if n >= MaxUploadsPerUser {
		return Upload{}, ErrTooManyUploads
	}
	if free := s.opts.FreeBytes(s.uploadsDir()); free >= 0 && size+spaceMargin > free {
		return Upload{}, fmt.Errorf("%w (%s free)", ErrNoSpace, humanize.Bytes(free))
	}
	id := ids.New()
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
		u, err := inspect(pr)
		if err != nil {
			_ = pr.CloseWithError(err)
		} else {
			_, _ = io.Copy(io.Discard, pr) // gzip trailer, tar padding
		}
		done <- result{u, err}
	}()
	h := sha256.New()
	written, cerr := io.Copy(io.MultiWriter(f, h, pw), io.LimitReader(body, size))
	_ = pw.Close()
	res := <-done
	switch {
	case res.err != nil:
		return Upload{}, res.err
	case cerr != nil:
		if ctx.Err() != nil {
			return Upload{}, ctx.Err()
		}
		return Upload{}, fmt.Errorf("%w: %w", ErrUploadIncomplete, cerr)
	case written != size:
		return Upload{}, ErrUploadIncomplete
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

// inspect reads a whole archive and measures its parts.
func inspect(r io.Reader) (Upload, error) {
	rd, err := NewReader(r)
	if err != nil {
		return Upload{}, err
	}
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

// DeleteUpload discards p's upload.
func (s *Service) DeleteUpload(p authz.Principal, id string) error {
	if _, err := s.Upload(p, id); err != nil {
		return err
	}
	s.mu.Lock()
	_, used := s.inUse[id]
	s.mu.Unlock()
	if used {
		return ErrUploadInUse
	}
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
