package files

import (
	"context"
	"encoding/json"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
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
		protocol.StreamFilesDownload: s.Download,
		protocol.StreamFilesUpload:   s.Upload,
	}
}
