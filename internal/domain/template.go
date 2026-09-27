package domain

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Stack templates (template registry): a complete Compose project (its
// definition files and the files next to them, such as bind-mounted
// configuration) kept by the manager. A template has one editable draft
// (a directory in the manager's data directory, edited with the file
// manager) and immutable published versions. Private templates are used
// on this instance only; public ones are also listed in the instance's
// public registry for other managers.

// TemplateVisibility says who can see a template.
type TemplateVisibility string

// Visibilities.
const (
	TemplatePrivate TemplateVisibility = "private"
	TemplatePublic  TemplateVisibility = "public"
)

// Valid reports whether v is a known visibility.
func (v TemplateVisibility) Valid() bool { return v == TemplatePrivate || v == TemplatePublic }

// Template limits.
const (
	MaxTemplateName        = 100
	MaxTemplateDescription = 1024
	MaxTemplateTags        = 16
	MaxTemplateTag         = 32
	MaxTemplateVersionNote = 4096
	MaxTemplateLabel       = 32
	// MaxTemplateIcon bounds an icon's bytes.
	MaxTemplateIcon = 256 << 10
)

var (
	templateTagRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	templateLabelRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]*$`)
)

// ValidTemplateTag reports whether t is a tag: lowercase letters, digits
// and dashes, starting with a letter or digit, at most MaxTemplateTag.
func ValidTemplateTag(t string) bool {
	return len(t) <= MaxTemplateTag && templateTagRE.MatchString(t)
}

// NormalizeTemplateTags lowercases, trims, deduplicates and sorts tags; it
// reports the first invalid tag.
func NormalizeTemplateTags(tags []string) ([]string, error) {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if !ValidTemplateTag(t) {
			return nil, &FieldError{Field: "tags", Message: "tags use lowercase letters, digits and dashes (at most 32 characters): " + t}
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > MaxTemplateTags {
		return nil, &FieldError{Field: "tags", Message: "at most 16 tags"}
	}
	slices.Sort(out)
	return out, nil
}

// ValidTemplateLabel reports whether l is a version label (1.2.0, 2024-06,
// v3-beta): letters, digits and . + _ -, starting with a letter or digit.
func ValidTemplateLabel(l string) bool {
	return len(l) <= MaxTemplateLabel && templateLabelRE.MatchString(l)
}

// TemplateIcon describes a template's icon (the bytes are read separately).
type TemplateIcon struct {
	MediaType string
	// SHA256 identifies the icon's bytes (hex); it changes with the icon.
	SHA256    string
	Size      int64
	UpdatedAt time.Time
}

// Template is one template of this instance.
type Template struct {
	ID          string
	Name        string
	Description string
	Tags        []string
	Visibility  TemplateVisibility
	Icon        *TemplateIcon
	// Latest is the newest published version (nil before the first).
	Latest *TemplateVersion
	// Versions counts the published versions.
	Versions        int
	CreatedByUserID string
	Revision        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TemplateFile is one file of a published version's definition.
type TemplateFile struct {
	Path string
	Size int64
}

// TemplateVersion is one immutable published state of a template's
// draft. Its archive (a canonical tar.gz of the draft) is stored sealed and
// read separately.
type TemplateVersion struct {
	ID         string
	TemplateID string
	// Number increases with every publication (never reused).
	Number int
	Label  string
	Notes  string
	// ArchiveSHA256 and ArchiveSize describe the tar.gz bytes; ContentSize
	// and Entries what it unpacks to.
	ArchiveSHA256 string
	ArchiveSize   int64
	ContentSize   int64
	Entries       int
	// Definition lists the Compose files and .env at the root.
	Definition        []TemplateFile
	PublishedByUserID string
	CreatedAt         time.Time
}

// TemplateInput creates a template.
type TemplateInput struct {
	Name        string
	Description string
	Tags        []string
}

// TemplatePatch edits a template's metadata; nil fields are unchanged.
type TemplatePatch struct {
	Name        *string
	Description *string
	Tags        *[]string
}

// TemplateFilter narrows template lists.
type TemplateFilter struct {
	// Query matches the name, description or a tag (case-insensitive).
	Query      string
	Tag        string
	Visibility TemplateVisibility
}

// Matches reports whether t passes the filter.
func (f TemplateFilter) Matches(t Template) bool {
	if f.Visibility != "" && t.Visibility != f.Visibility {
		return false
	}
	if f.Tag != "" && !slices.Contains(t.Tags, f.Tag) {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		if !strings.Contains(strings.ToLower(t.Name), q) && !strings.Contains(strings.ToLower(t.Description), q) &&
			!slices.ContainsFunc(t.Tags, func(tag string) bool { return strings.Contains(tag, q) }) {
			return false
		}
	}
	return true
}

// Template errors.
var (
	ErrTemplateNotFound          = errors.New("template not found")
	ErrTemplateNameTaken         = errors.New("another template already uses this name")
	ErrTemplateVersionNotFound   = errors.New("template version not found")
	ErrTemplateVersionLabelTaken = errors.New("another version of the template already uses this label")
	ErrTemplateIconNotFound      = errors.New("the template has no icon")
	// ErrTemplatePublicAckRequired: making a template public (or publishing
	// a version of a public one) needs the explicit acknowledgement that
	// every file, .env included, becomes readable by anyone.
	ErrTemplatePublicAckRequired = errors.New("acknowledge that every file of the template, .env included, becomes public")
)

// TemplateTooLargeError refuses a change that would make the draft exceed
// the template size or entry limit.
type TemplateTooLargeError struct {
	Message string
}

func (e *TemplateTooLargeError) Error() string { return e.Message }

// TemplateIconError refuses an icon (unsupported type or too large).
type TemplateIconError struct {
	TooLarge bool
	Message  string
}

func (e *TemplateIconError) Error() string { return e.Message }

// TemplateDefinitionError refuses publishing a draft that is not a usable
// Compose project (no Compose file, a pinned project name, special files).
type TemplateDefinitionError struct {
	Message string
}

func (e *TemplateDefinitionError) Error() string { return e.Message }
