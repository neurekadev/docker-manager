package templates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// maxDefinitionFile bounds one Compose source read from an archive (the
// stack definition limit, protocol.MaxSourceFile).
const maxDefinitionFile = 256 << 10

// DefinitionOf reads the Compose sources at the root of a version archive,
// in path order. Files larger than the stack definition limit are refused.
func DefinitionOf(archive []byte) ([]domain.TemplateFileContent, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("templates: read the archive: %w", err)
	}
	tr := tar.NewReader(zr)
	var out []domain.TemplateFileContent
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("templates: read the archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || !IsDefinitionFile(h.Name) {
			continue
		}
		if h.Size > maxDefinitionFile {
			return nil, &domain.TemplateDefinitionError{Message: fmt.Sprintf("%s is larger than 256 KiB", h.Name)}
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxDefinitionFile+1))
		if err != nil {
			return nil, fmt.Errorf("templates: read the archive: %w", err)
		}
		out = append(out, domain.TemplateFileContent{Path: h.Name, Content: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Definition returns the Compose sources of a published version.
func (s *Service) Definition(ctx context.Context, id string, number int) (domain.TemplateVersion, []domain.TemplateFileContent, error) {
	v, b, err := s.Archive(ctx, id, number)
	if err != nil {
		return v, nil, err
	}
	files, err := DefinitionOf(b)
	return v, files, err
}
