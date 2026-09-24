// Package ids generates DockYard's stable resource identifiers.
//
// Every DockYard-owned record (instance, user, environment, stack, job, ...)
// uses a UUIDv7 string: time-ordered (good SQLite index locality), opaque to
// clients and never derived from Docker IDs, host names or file paths.
package ids

import (
	"fmt"

	"github.com/google/uuid"
)

// New returns a new UUIDv7 string.
func New() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Only fails if the system CSPRNG fails; nothing sensible to do.
		panic(fmt.Sprintf("ids: generate UUIDv7: %v", err))
	}
	return id.String()
}

// Valid reports whether s is a canonical lowercase UUID string.
func Valid(s string) bool {
	if len(s) != 36 {
		return false
	}
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s
}
