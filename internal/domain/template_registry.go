package domain

import (
	"errors"
	"time"
)

// Template registries (template registry): other Docker Manager instances
// whose public templates this instance browses and creates stacks from.
// A registry is identified by the remote instance's ID; its templates are
// a cached copy of its index, refreshed by syncs.

// TemplateRegistryStatus is the state of the last sync.
type TemplateRegistryStatus string

// Registry statuses.
const (
	TemplateRegistryOK    TemplateRegistryStatus = "ok"
	TemplateRegistryError TemplateRegistryStatus = "error"
)

// TemplateRegistry is an added registry.
type TemplateRegistry struct {
	// InstanceID is the remote instance's ID (the registry's identity).
	InstanceID string
	// URL is the registry's origin (https://host[:port]).
	URL    string
	Name   string
	Status TemplateRegistryStatus
	// ErrorClass and ErrorMessage describe the last failed sync.
	ErrorClass   string
	ErrorMessage string
	ETag         string
	// Failures counts consecutive failed syncs (backoff).
	Failures      int
	SyncedAt      *time.Time
	AttemptedAt   *time.Time
	AddedByUserID string
	// Templates counts the cached templates.
	Templates int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RegistryTemplateVersion is a published version of a registry template.
type RegistryTemplateVersion struct {
	Number        int
	Label         string
	Notes         string
	PublishedAt   time.Time
	ArchiveSHA256 string
	ArchiveSize   int64
	ContentSize   int64
	Entries       int
	// ArchiveURL is the archive's path on the registry.
	ArchiveURL string
}

// RegistryTemplate is a cached template of a registry.
type RegistryTemplate struct {
	RegistryID  string
	TemplateID  string
	Name        string
	Description string
	Tags        []string
	// IconSHA256 is empty when the template has no (usable) icon.
	IconSHA256 string
	IconURL    string
	// Versions are newest first.
	Versions  []RegistryTemplateVersion
	UpdatedAt time.Time
}

// Version returns a version by number (0: the newest).
func (t RegistryTemplate) Version(number int) (RegistryTemplateVersion, bool) {
	for _, v := range t.Versions {
		if number == 0 || v.Number == number {
			return v, true
		}
	}
	return RegistryTemplateVersion{}, false
}

// Template registry errors.
var (
	ErrTemplateRegistryNotFound = errors.New("template registry not found")
	ErrTemplateRegistryIsSelf   = errors.New("this is this instance's own registry")
	ErrTemplateRegistryExists   = errors.New("this registry is added already")
)
