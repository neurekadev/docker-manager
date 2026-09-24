package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Pagination conventions for every list route (#4):
//
//   - Requests embed PageParams: ?cursor=<opaque>&limit=<1..200, default 50>.
//   - Responses return Page[T]. nextCursor is absent on the last page.
//   - Cursors are opaque to clients (base64url JSON produced by EncodeCursor);
//     they encode the sort key of the last item, never an offset, so pages
//     stay stable while items are inserted.
//   - total, when present, counts only items the caller may see (results and
//     aggregates are permission-filtered, #17). Routes that cannot compute it
//     cheaply omit it.
//
// TODO(#4): filter/sort parameter conventions.

// Page is the response body of every list operation.
type Page[T any] struct {
	Items      []T    `json:"items" doc:"Items on this page (possibly empty)."`
	NextCursor string `json:"nextCursor,omitempty" doc:"Opaque cursor for the next page; absent on the last page."`
	Total      *int64 `json:"total,omitempty" doc:"Number of items visible to the caller across all pages, when cheap to compute."`
}

// NewPage builds a page, normalizing nil items to an empty list.
func NewPage[T any](items []T, nextCursor string, total *int64) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, NextCursor: nextCursor, Total: total}
}

// Default and maximum page sizes.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// PageParams is embedded in list operation inputs.
type PageParams struct {
	Cursor string `query:"cursor" maxLength:"512" doc:"Opaque cursor from a previous page's nextCursor."`
	Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Maximum number of items to return."`
}

// EncodeCursor serializes a cursor position (typically the last item's sort key).
func EncodeCursor(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("api: encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DecodeCursor parses a cursor into v, returning a 422 Error when malformed.
func DecodeCursor(cursor string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || json.Unmarshal(b, v) != nil {
		return Invalid("invalid cursor", Field("query.cursor", "cursor is malformed or expired"))
	}
	return nil
}
