package engine

import (
	"context"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ListImages lists images (all includes intermediate images).
func (c *Client) ListImages(ctx context.Context, all bool) ([]Image, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.ImageList(ctx, client.ImageListOptions{All: all})
	if err != nil {
		return nil, wrap("image.list", err)
	}
	out := make([]Image, 0, len(res.Items))
	for _, s := range res.Items {
		out = append(out, Image{
			ID: s.ID, RepoTags: s.RepoTags, RepoDigests: s.RepoDigests,
			Created: time.Unix(s.Created, 0).UTC(), Size: s.Size, Containers: s.Containers, Labels: s.Labels,
		})
	}
	return out, nil
}

// InspectImage returns an image's details.
func (c *Client) InspectImage(ctx context.Context, ref string) (ImageDetails, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.ImageInspect(ctx, ref)
	if err != nil {
		return ImageDetails{}, wrap("image.inspect", err)
	}
	r := res.InspectResponse
	d := ImageDetails{
		ID: r.ID, RepoTags: r.RepoTags, RepoDigests: r.RepoDigests, Created: parseTime(r.Created),
		Size: r.Size, OS: r.Os, Architecture: r.Architecture, Variant: r.Variant, Author: r.Author,
	}
	if cfg := r.Config; cfg != nil {
		d.Labels = cfg.Labels
		d.Entrypoint = cfg.Entrypoint
		d.Cmd = cfg.Cmd
		d.WorkingDir = cfg.WorkingDir
		d.User = cfg.User
		for p := range cfg.ExposedPorts {
			d.ExposedPorts = append(d.ExposedPorts, p)
		}
		sort.Strings(d.ExposedPorts)
		for v := range cfg.Volumes {
			d.Volumes = append(d.Volumes, v)
		}
		sort.Strings(d.Volumes)
		d.HasHealthTest = cfg.Healthcheck != nil && len(cfg.Healthcheck.Test) > 0 && cfg.Healthcheck.Test[0] != "NONE"
	}
	return d, nil
}

// encodeAuth returns the X-Registry-Auth value for a. The encoded value
// lives only in the request; it is never stored or logged.
func encodeAuth(a *RegistryAuth) (string, error) {
	if a == nil {
		return "", nil
	}
	return authconfig.Encode(registry.AuthConfig{
		Username:      a.Username,
		Password:      string(a.Password),
		IdentityToken: string(a.IdentityToken),
		ServerAddress: a.ServerAddress,
	})
}

// PullImage pulls ref with an optional per-operation credential. It
// streams progress to o.Progress and returns once the pull completed or
// failed. Registry failures map to CodeUnauthorized, CodeForbidden,
// CodeRateLimited, CodeNotFound or CodeRegistryUnavailable.
func (c *Client) PullImage(ctx context.Context, ref string, o PullOptions) (PullResult, error) {
	const op = "image.pull"
	auth, err := encodeAuth(o.Auth)
	if err != nil {
		return PullResult{}, &Error{Op: op, Code: CodeInvalidArgument, Message: "invalid registry credential", err: err}
	}
	opts := client.ImagePullOptions{RegistryAuth: auth}
	if o.Platform != "" {
		p, err := parsePlatform(o.Platform)
		if err != nil {
			return PullResult{}, &Error{Op: op, Code: CodeInvalidArgument, Message: err.Error(), err: err}
		}
		opts.Platforms = []ocispec.Platform{p}
	}
	resp, err := c.api.ImagePull(ctx, ref, opts)
	if err != nil {
		return PullResult{}, wrap(op, err)
	}
	defer resp.Close()
	var res PullResult
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return PullResult{}, wrap(op, err)
		}
		if msg.Error != nil {
			return PullResult{}, &Error{Op: op, Code: pullErrorCode(msg.Error.Code, msg.Error.Message), Message: msg.Error.Message}
		}
		if d, ok := strings.CutPrefix(msg.Status, "Digest: "); ok {
			res.Digest = strings.TrimSpace(d)
		}
		if o.Progress != nil {
			p := Progress{ID: msg.ID, Status: msg.Status}
			if msg.Progress != nil {
				p.Current, p.Total = msg.Progress.Current, msg.Progress.Total
			}
			o.Progress(p)
		}
	}
	if err := ctx.Err(); err != nil {
		return PullResult{}, wrap(op, err)
	}
	if d, err := c.InspectImage(ctx, ref); err == nil {
		res.ImageID = d.ID
	}
	return res, nil
}

func pullErrorCode(status int, msg string) Code {
	if code := classifyRegistryMessage(msg); code != "" {
		return code
	}
	switch status {
	case 401:
		return CodeUnauthorized
	case 403:
		return CodeForbidden
	case 404:
		return CodeNotFound
	case 429:
		return CodeRateLimited
	}
	return CodeEngineError
}

// TagImage adds target as a reference to source.
func (c *Client) TagImage(ctx context.Context, source, target string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.ImageTag(ctx, client.ImageTagOptions{Source: source, Target: target})
	return wrap("image.tag", err)
}

// RemoveImage removes an image reference (and the image when unreferenced).
func (c *Client) RemoveImage(ctx context.Context, ref string, force, pruneChildren bool) ([]DeletedImage, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.ImageRemove(ctx, ref, client.ImageRemoveOptions{Force: force, PruneChildren: pruneChildren})
	if err != nil {
		return nil, wrap("image.remove", err)
	}
	out := make([]DeletedImage, 0, len(res.Items))
	for _, d := range res.Items {
		out = append(out, DeletedImage{Untagged: d.Untagged, Deleted: d.Deleted})
	}
	return out, nil
}

// LoadImage loads an image archive (docker save / OCI layout tar) into the
// Engine, e.g. for environment migration (#35).
func (c *Client) LoadImage(ctx context.Context, archive io.Reader) error {
	const op = "image.load"
	res, err := c.api.ImageLoad(ctx, archive, client.ImageLoadWithQuiet(true))
	if err != nil {
		return wrap(op, err)
	}
	defer res.Close()
	return consumeJSONStream(ctx, op, res)
}
