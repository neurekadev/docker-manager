package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// List conventions for every list route (#4); docs/internal/api/conventions.md is the
// client-facing description.
//
//   - Pagination: inputs embed PageParams (?cursor=&limit=, default 50, max
//     200). Responses are Page[T]; nextCursor is absent on the last page.
//     Cursors are opaque (EncodeCursor/DecodeCursor), encode the sort key of
//     the last item (never an offset) so pages stay stable while items are
//     inserted, and are bound to the filters and sort they were issued for
//     (CursorFor/DecodeCursorFor): reusing one with different filters is a
//     422 on query.cursor.
//   - Filters: plain query parameters named after the item field
//     (?state=running&state=failed, ?environmentId=...). Repeating a
//     parameter ORs its values; different parameters AND. Free-text search
//     uses ?q=. Unknown filter values are 422, unknown parameters are
//     rejected by the schema.
//   - Sort: ?sort=field,-other (a leading - sorts descending), parsed by
//     ParseSort against the route's documented fields. Every route has a
//     documented default order and breaks ties by ID so the order is total.
//   - Visibility: items are filtered per item by the route's capability
//     (#17); callers never learn about items they may not see. ScanPage
//     implements the bounded filter-while-paging loop, so a page may hold
//     fewer than limit items while nextCursor is present.
//   - total: present only on routes that document it, and then counts the
//     items matching the filters that the caller may see (never the raw
//     number, so aggregates do not leak). Routes whose visibility filtering
//     makes counting expensive omit it (audit events) or send it only
//     when it is exact (jobs: a COUNT for the owner, a per-job check of
//     at most 1000 matches for other callers, absent above that).

// Page is the response body of every list operation.
type Page[T any] struct {
	Items      []T    `json:"items" doc:"Items on this page (possibly empty, also when nextCursor is present)."`
	NextCursor string `json:"nextCursor,omitempty" doc:"Opaque cursor for the next page; absent on the last page."`
	Total      *int64 `json:"total,omitempty" doc:"Number of items matching the filters that the caller may see, across all pages. Only on routes that document it."`
}

// NewPage builds a page, normalizing nil items to an empty list.
func NewPage[T any](items []T, nextCursor string, total *int64) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, NextCursor: nextCursor, Total: total}
}

// Total returns a total count for NewPage.
func Total(n int64) *int64 { return &n }

// Default and maximum page sizes.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// PageParams is embedded in list operation inputs.
type PageParams struct {
	Cursor string `query:"cursor" maxLength:"512" doc:"Opaque cursor from a previous page's nextCursor. Only valid with the same filters and sort."`
	Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Maximum number of items to return."`
}

// PageLimit returns the effective page size.
func (p PageParams) PageLimit() int {
	switch {
	case p.Limit <= 0:
		return DefaultPageLimit
	case p.Limit > MaxPageLimit:
		return MaxPageLimit
	}
	return p.Limit
}

// SortParam is embedded in inputs of lists that offer several orders. The
// route documents its sortable fields and default order in its description
// and validates the value with ParseSort.
type SortParam struct {
	Sort string `query:"sort" maxLength:"256" pattern:"^-?[A-Za-z][A-Za-z0-9]*(,-?[A-Za-z][A-Za-z0-9]*)*$" doc:"Comma-separated sort fields; a leading - sorts descending (e.g. -createdAt,name). Allowed fields are listed in the operation description."`
}

// SortKey is one parsed sort field.
type SortKey struct {
	Field string
	Desc  bool
}

// String renders the key in query syntax.
func (k SortKey) String() string {
	if k.Desc {
		return "-" + k.Field
	}
	return k.Field
}

// ParseSort parses a sort parameter. An empty value yields def. Every field
// must be in allowed and appear at most once; errors are 422 on query.sort.
func ParseSort(raw string, allowed []string, def ...SortKey) ([]SortKey, error) {
	if raw == "" {
		return slices.Clone(def), nil
	}
	var out []SortKey
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		k := SortKey{Field: part}
		if strings.HasPrefix(part, "-") {
			k = SortKey{Field: part[1:], Desc: true}
		}
		if !slices.Contains(allowed, k.Field) {
			return nil, Invalid("invalid sort", Field("query.sort", fmt.Sprintf("unknown sort field %q; allowed: %s", k.Field, strings.Join(allowed, ", "))))
		}
		if seen[k.Field] {
			return nil, Invalid("invalid sort", Field("query.sort", fmt.Sprintf("field %q appears twice", k.Field)))
		}
		seen[k.Field] = true
		out = append(out, k)
	}
	return out, nil
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
		return invalidCursor()
	}
	return nil
}

func invalidCursor() *Error {
	return Invalid("invalid cursor", Field("query.cursor", "cursor is malformed, expired or was issued for different filters or sort"))
}

// QueryFingerprint identifies a list query (filters and sort, not the
// cursor or limit) so a cursor cannot be replayed against another query.
func QueryFingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

type boundCursor struct {
	Q string          `json:"q"`
	P json.RawMessage `json:"p"`
}

// CursorFor encodes position bound to a query fingerprint.
func CursorFor(fingerprint string, position any) (string, error) {
	p, err := json.Marshal(position)
	if err != nil {
		return "", fmt.Errorf("api: encode cursor: %w", err)
	}
	return EncodeCursor(boundCursor{Q: fingerprint, P: p})
}

// DecodeCursorFor decodes a cursor from CursorFor into position; a cursor
// issued for another fingerprint is a 422 on query.cursor.
func DecodeCursorFor(cursor, fingerprint string, position any) error {
	var c boundCursor
	if err := DecodeCursor(cursor, &c); err != nil {
		return err
	}
	if c.Q != fingerprint || len(c.P) == 0 || json.Unmarshal(c.P, position) != nil {
		return invalidCursor()
	}
	return nil
}

// DefaultMaxScans bounds how many batches ScanPage reads per request.
const DefaultMaxScans = 10

// Scan describes a filter-while-paging list over records R kept in a
// stable order by a string position key (typically the ID or a sort key).
type Scan[R any] struct {
	// Limit is the page size (PageParams.PageLimit()).
	Limit int
	// After is the position to continue after ("" = from the start).
	After string
	// Fetch returns up to n records strictly after position after, in order.
	Fetch func(ctx context.Context, after string, n int) ([]R, error)
	// Position returns the record's position key.
	Position func(R) string
	// Visible reports whether the caller may see the record (#17).
	Visible func(R) bool
	// MaxScans bounds the batches read (default DefaultMaxScans), so a
	// caller who can see few records cannot make one request scan the table.
	MaxScans int
}

// ScanPage runs s and returns the visible records of one page and the
// position to continue from ("" on the last page). When the scan budget runs
// out before the page is full, the page is short but next is set: clients
// keep following nextCursor.
func ScanPage[R any](ctx context.Context, s Scan[R]) (items []R, next string, err error) {
	limit := s.Limit
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	maxScans := s.MaxScans
	if maxScans <= 0 {
		maxScans = DefaultMaxScans
	}
	scanned, exhausted := s.After, false
	for scans := 0; scans < maxScans && len(items) <= limit && !exhausted; scans++ {
		batch, err := s.Fetch(ctx, scanned, limit+1)
		if err != nil {
			return nil, "", err
		}
		exhausted = len(batch) < limit+1
		for _, r := range batch {
			if len(items) > limit {
				break
			}
			scanned = s.Position(r)
			if s.Visible(r) {
				items = append(items, r)
			}
		}
	}
	switch {
	case len(items) > limit:
		items = items[:limit]
		next = s.Position(items[limit-1])
	case !exhausted && scanned != s.After:
		next = scanned
	}
	return items, next, nil
}
