package files

import (
	"context"
	"encoding/json"

	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
)

// decode strictly decodes a request input.
func decode[T any](input json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(input, &v); err != nil {
		return v, fail(protocol.CodeInvalidFrame, "malformed input")
	}
	return v, nil
}

func handler[I, O any](fn func(context.Context, I) (O, error)) session.RequestHandler {
	return func(ctx context.Context, input json.RawMessage) (any, error) {
		in, err := decode[I](input)
		if err != nil {
			return nil, err
		}
		out, err := fn(ctx, in)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
}

// Requests returns the files.* request handlers
// (docs/internal/protocol/agent-v1.md, "Allowed requests").
func (s *Service) Requests() map[string]session.RequestHandler {
	return map[string]session.RequestHandler{
		protocol.ReqFilesList:            handler(s.List),
		protocol.ReqFilesStat:            handler(s.Stat),
		protocol.ReqFilesRead:            handler(s.Read),
		protocol.ReqFilesWrite:           handler(s.Write),
		protocol.ReqFilesMkdir:           handler(s.Mkdir),
		protocol.ReqFilesConflictPreview: handler(s.Preview),
	}
}

// Streams returns the files.download and files.upload stream handlers.
func (s *Service) Streams() map[string]session.StreamHandler {
	return map[string]session.StreamHandler{
		protocol.StreamFilesDownload: s.download,
		protocol.StreamFilesUpload:   s.upload,
	}
}

// download streams one file or an archive (files.download).
func (s *Service) download(ctx context.Context, st *streammux.Stream) error {
	var in protocol.FilesDownloadInput
	if err := json.Unmarshal(st.Input(), &in); err != nil {
		return fail(protocol.CodeInvalidFrame, "malformed download input")
	}
	if err := s.Download(ctx, in, st); err != nil {
		return err
	}
	return st.CloseWrite()
}

// upload writes the stream's bytes to a file (files.upload); the result
// travels in the final stream_close.
func (s *Service) upload(ctx context.Context, st *streammux.Stream) error {
	var in protocol.FilesUploadInput
	if err := json.Unmarshal(st.Input(), &in); err != nil {
		return fail(protocol.CodeInvalidFrame, "malformed upload input")
	}
	res, err := s.Upload(ctx, in, st)
	if err != nil {
		return err
	}
	return st.CloseWithResult(res)
}
