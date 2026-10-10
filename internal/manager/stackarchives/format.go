package stackarchives

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// The stack archive format (version 1): one tar, gzip-compressed, holding
//
//	docker-manager-stack.json   the manifest (first member)
//	project/...                 the project directory
//	volumes/<key>/...           each included volume, by Compose key
//
// in that order, each part contiguous. A part is the PAX tar the agents'
// migration transfer writes (root directory first, numeric owners,
// permission and special bits, times, symlinks as links, hard links,
// FIFOs) with its member names (and hard link targets) moved below the
// part's prefix, so an archive is an ordinary tar.gz any tool opens, and
// the reader turns each part back into exactly that transfer tar.

// Format identifiers.
const (
	FormatName    = "docker-manager-stack"
	FormatVersion = 1
	// ManifestName is the first member.
	ManifestName = "docker-manager-stack.json"
	// MaxManifest bounds the manifest.
	MaxManifest = 1 << 20
	// FileExtension is the archive file's extension.
	FileExtension = ".tar.gz"
)

// Part prefixes.
const (
	projectPrefix = "project"
	volumesPrefix = "volumes"
)

// Manifest describes an archive.
type Manifest struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exportedAt"`
	// ManagerVersion is the exporting manager's version (informational).
	ManagerVersion string        `json:"managerVersion,omitempty"`
	Stack          ManifestStack `json:"stack"`
	Project        PartStats     `json:"project"`
	// Volumes are the included volumes, in archive order.
	Volumes []ManifestVolume `json:"volumes"`
	// NotIncluded lists what the archive leaves out (volumes and the data
	// outside the project directory), with the reason.
	NotIncluded []Exclusion `json:"notIncluded"`
	// Services, Networks and ExternalVolumes describe the definition at
	// export, so an import is checked before anything is written.
	Services        []ManifestService `json:"services"`
	Networks        []ManifestNetwork `json:"networks"`
	ExternalVolumes []string          `json:"externalVolumes"`
}

// ManifestStack is the exported stack.
type ManifestStack struct {
	// Name is the Compose project name at export.
	Name        string        `json:"name"`
	DisplayName string        `json:"displayName,omitempty"`
	Description string        `json:"description,omitempty"`
	Links       []domain.Link `json:"links,omitempty"`
	// ConfigFiles and EnvFiles are the project-relative Compose and env
	// files the stack loads (none: Compose's defaults).
	ConfigFiles []string `json:"configFiles,omitempty"`
	EnvFiles    []string `json:"envFiles,omitempty"`
}

// ManifestVolume is an included volume.
type ManifestVolume struct {
	// Key is the Compose volume key: the part is volumes/<key>/.
	Key string `json:"key"`
	// Name is the volume's name at export; FollowsProject: it is
	// <project>_<key>, so a stack created under another name renames it.
	Name           string `json:"name"`
	FollowsProject bool   `json:"followsProject"`
	// Labels are the volume's labels other than Compose's and Docker
	// Manager's own (user-set ones, such as backup exclusions).
	Labels map[string]string `json:"labels,omitempty"`
	PartStats
}

// ManifestService is a service of the definition.
type ManifestService struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
	Build bool   `json:"build,omitempty"`
	// Registry: the image came from a registry (it has a repository
	// digest), so the destination can pull it.
	Registry       bool                     `json:"registry,omitempty"`
	ContainerNames []string                 `json:"containerNames,omitempty"`
	Ports          []protocol.MigrationPort `json:"ports,omitempty"`
}

// ManifestNetwork is a network of the definition.
type ManifestNetwork struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	External bool   `json:"external,omitempty"`
}

// Exclusion is something an archive leaves out.
type Exclusion struct {
	// Kind is volume, anonymous_volume or bind.
	Kind string `json:"kind"`
	// Key is a volume's Compose key.
	Key    string `json:"key,omitempty"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Exclusion kinds.
const (
	ExcludedVolume    = "volume"
	ExcludedAnonymous = "anonymous_volume"
	ExcludedBind      = "bind"
)

// PartStats counts a part: its members and the bytes of its regular
// files.
type PartStats struct {
	Entries int64 `json:"entries"`
	Bytes   int64 `json:"bytes"`
}

// ErrInvalid wraps every reason an archive is refused.
var ErrInvalid = errors.New("not a valid stack archive")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// volumeKeyRE is a Compose volume key usable as one path segment.
var volumeKeyRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$`)

// Validate checks a manifest's shape (an archive's manifest is untrusted).
func (m Manifest) Validate() error {
	switch {
	case m.Format != FormatName:
		return invalidf("the manifest's format is %q, not %q", m.Format, FormatName)
	case m.Version != FormatVersion:
		return invalidf("archive version %d is not supported (this manager reads version %d)", m.Version, FormatVersion)
	case !protocol.ValidProjectName(m.Stack.Name):
		return invalidf("invalid stack name %q", m.Stack.Name)
	case len(m.Volumes) > 64:
		return invalidf("more than 64 volumes")
	case len(m.Services) > 256 || len(m.Networks) > 256 || len(m.ExternalVolumes) > 256 || len(m.NotIncluded) > 512:
		return invalidf("the manifest lists too many services, networks or exclusions")
	}
	for _, f := range append(slices.Clone(m.Stack.ConfigFiles), m.Stack.EnvFiles...) {
		if !protocol.ValidRelativePath(f) || f == "." {
			return invalidf("invalid definition file %q", f)
		}
	}
	keys, names := map[string]bool{}, map[string]bool{}
	for _, v := range m.Volumes {
		switch {
		case !volumeKeyRE.MatchString(v.Key) || v.Key == "." || v.Key == "..":
			return invalidf("invalid volume key %q", v.Key)
		case !protocol.ValidDockerName(v.Name):
			return invalidf("invalid volume name %q", v.Name)
		case keys[v.Key] || names[v.Name]:
			return invalidf("volume %s is listed twice", v.Key)
		case v.FollowsProject && v.Name != m.Stack.Name+"_"+v.Key:
			return invalidf("volume %s does not follow the project name", v.Key)
		}
		keys[v.Key], names[v.Name] = true, true
		for k := range v.Labels {
			if strings.HasPrefix(k, "com.docker.compose.") || protocol.OwnLabel(k) {
				return invalidf("volume %s carries the label %q", v.Key, k)
			}
		}
		if len(v.Labels) > 32 {
			return invalidf("volume %s has too many labels", v.Key)
		}
	}
	return nil
}

// Volume returns the included volume with key.
func (m Manifest) Volume(key string) (ManifestVolume, bool) {
	i := slices.IndexFunc(m.Volumes, func(v ManifestVolume) bool { return v.Key == key })
	if i < 0 {
		return ManifestVolume{}, false
	}
	return m.Volumes[i], true
}

// Part names an archive part.
type Part struct {
	// Volume is the volume key ("" for the project).
	Volume string
}

// Name is the part's name in job items ("project", "volume:<key>").
func (p Part) Name() string {
	if p.Volume == "" {
		return protocol.PartProject
	}
	return "volume:" + p.Volume
}

func (p Part) prefix() string {
	if p.Volume == "" {
		return projectPrefix
	}
	return volumesPrefix + "/" + p.Volume
}

// Writer writes an archive.
type Writer struct {
	gz *gzip.Writer
	tw *tar.Writer
}

// NewWriter starts an archive on w with its manifest. Add the project
// first, then the manifest's volumes in its order.
func NewWriter(w io.Writer, m Manifest) (*Writer, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(b) > MaxManifest {
		return nil, fmt.Errorf("stackarchives: the manifest exceeds %d bytes", MaxManifest)
	}
	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	aw := &Writer{gz: gz, tw: tar.NewWriter(gz)}
	hdr := &tar.Header{Format: tar.FormatPAX, Typeflag: tar.TypeReg, Name: ManifestName, Mode: 0o644, Size: int64(len(b)),
		ModTime: m.ExportedAt}
	if err := aw.tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := aw.tw.Write(b); err != nil {
		return nil, err
	}
	return aw, nil
}

// AddPart copies a transfer tar (root "./" first, as migration.send
// writes it) into the archive below the part's prefix.
func (w *Writer) AddPart(p Part, r io.Reader) (PartStats, error) {
	var st PartStats
	tr := tar.NewReader(r)
	prefix := p.prefix()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return st, err
		}
		name, ok := cleanName(hdr.Name)
		if !ok {
			return st, fmt.Errorf("stackarchives: %s: invalid member name %q", p.Name(), hdr.Name)
		}
		if (name == ".") != (st.Entries == 0) {
			return st, fmt.Errorf("stackarchives: %s: the root directory must be the first member", p.Name())
		}
		st.Entries++
		out := *hdr
		out.Format = tar.FormatPAX
		out.Name = path.Join(prefix, name)
		if hdr.Typeflag == tar.TypeDir {
			out.Name += "/"
		}
		if hdr.Typeflag == tar.TypeLink {
			target, ok := cleanName(hdr.Linkname)
			if !ok || target == "." {
				return st, fmt.Errorf("stackarchives: %s: invalid hard link target %q", p.Name(), hdr.Linkname)
			}
			out.Linkname = path.Join(prefix, target)
		}
		// Drop the source's own PAX records: names and links are new, and
		// archive/tar writes what the header needs.
		out.PAXRecords = nil
		if err := w.tw.WriteHeader(&out); err != nil {
			return st, err
		}
		if hdr.Typeflag == tar.TypeReg {
			n, err := io.Copy(w.tw, tr)
			if err != nil {
				return st, err
			}
			st.Bytes += n
		}
	}
	if st.Entries == 0 {
		return st, fmt.Errorf("stackarchives: %s: the part is empty", p.Name())
	}
	return st, nil
}

// Close ends the archive (it does not close the underlying writer).
func (w *Writer) Close() error {
	if err := w.tw.Close(); err != nil {
		return err
	}
	return w.gz.Close()
}

// Reader reads an archive sequentially: the manifest, then each part.
type Reader struct {
	tr       *tar.Reader
	m        Manifest
	pending  *tar.Header // the first member not consumed yet
	pendName string      // its clean name, without a leading "./"
	seen     map[string]bool
	project  bool
	eof      bool
	// compose holds the project's root Compose files read on the way.
	compose map[string][]byte
	// src is the (decompressed) tar stream.
	src *countedReader
	// limit bounds the bytes of the archive's files (0: none; SetLimit);
	// total counts them.
	limit int64
	total int64
}

// countedReader counts the decompressed bytes and refuses more than max
// (0: no bound): headers and padding count too, so no archive inflates
// without bound, whatever its members.
type countedReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (c *countedReader) Read(p []byte) (int, error) {
	if c.max > 0 && c.n > c.max {
		return 0, invalidf("the archive unpacks to more than %d bytes", c.max)
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// maxTrailing bounds what may follow the tar end marker (padding to a
// record, a tool's blocking factor).
const maxTrailing = 1 << 20

// SetLimit bounds the bytes of the archive's files to n, and the whole
// decompressed stream (headers, padding) to twice that plus a margin.
func (r *Reader) SetLimit(n int64) {
	r.limit = n
	r.src.max = 2*n + 64<<20
}

// Compose files read from the project's root (definition checks).
const (
	maxComposeFiles = 16
	maxComposeFile  = protocol.MaxSourceFile
)

var composeFileRE = regexp.MustCompile(`^(docker-)?compose(\.[^/]*)?\.ya?ml$`)

// ComposeFiles returns the project's root Compose files (and the stack's
// own configured ones at the root) read so far, at most 16 of up to
// protocol.MaxSourceFile bytes each.
func (r *Reader) ComposeFiles() map[string][]byte { return r.compose }

func (r *Reader) wantsCompose(p Part, inner string, size int64) bool {
	if p.Volume != "" || strings.Contains(inner, "/") || size > maxComposeFile || len(r.compose) >= maxComposeFiles {
		return false
	}
	return composeFileRE.MatchString(inner) || slices.Contains(r.m.Stack.ConfigFiles, inner)
}

// NewReader opens an archive (gzip-compressed or a plain tar) and reads
// and validates its manifest.
func NewReader(r io.Reader) (*Reader, error) {
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, invalidf("%s", err.Error())
		}
		src = gz
	}
	cnt := &countedReader{r: src}
	rd := &Reader{tr: tar.NewReader(cnt), src: cnt, seen: map[string]bool{}, compose: map[string][]byte{}}
	if err := rd.advance(); err != nil {
		return nil, err
	}
	if rd.pending == nil || rd.pendName != ManifestName || rd.pending.Typeflag != tar.TypeReg {
		return nil, invalidf("the first member must be %s", ManifestName)
	}
	if rd.pending.Size > MaxManifest {
		return nil, invalidf("the manifest exceeds %d bytes", MaxManifest)
	}
	b, err := io.ReadAll(io.LimitReader(rd.tr, MaxManifest+1))
	if err != nil {
		return nil, invalid(err)
	}
	if err := json.Unmarshal(b, &rd.m); err != nil {
		return nil, invalidf("the manifest is not valid JSON")
	}
	if err := rd.m.Validate(); err != nil {
		return nil, err
	}
	if err := rd.advance(); err != nil {
		return nil, err
	}
	return rd, nil
}

func invalid(err error) error {
	if errors.Is(err, ErrInvalid) {
		return err
	}
	return fmt.Errorf("%w: %s", ErrInvalid, err.Error())
}

// advance reads the next member, skipping the directory entries tools
// add above the parts ("./", "volumes/").
func (r *Reader) advance() error {
	for {
		hdr, err := r.tr.Next()
		if errors.Is(err, io.EOF) {
			r.pending, r.pendName, r.eof = nil, "", true
			return nil
		}
		if err != nil {
			return invalid(err)
		}
		name, ok := cleanName(hdr.Name)
		if !ok {
			return invalidf("invalid member name %q", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeDir && (name == "." || name == volumesPrefix) {
			continue
		}
		r.pending, r.pendName = hdr, name
		return nil
	}
}

// Close reads the archive to its end after the last part: the tar padding
// and, for a gzip-compressed archive, its trailer, whose checksum covers
// every byte (corruption the tar layout does not show).
func (r *Reader) Close() error {
	n, err := io.CopyN(io.Discard, r.src, maxTrailing+1)
	switch {
	case n > maxTrailing:
		return invalidf("more than %d bytes follow the archive's end", maxTrailing)
	case err != nil && !errors.Is(err, io.EOF):
		return invalid(err)
	}
	return nil
}

// Manifest returns the archive's manifest.
func (r *Reader) Manifest() Manifest { return r.m }

// partOf returns the part a clean member name belongs to and its name
// within the part.
func partOf(name string) (Part, string, bool) {
	if name == projectPrefix || strings.HasPrefix(name, projectPrefix+"/") {
		rest := strings.TrimPrefix(strings.TrimPrefix(name, projectPrefix), "/")
		return Part{}, rest, true
	}
	if rest, ok := strings.CutPrefix(name, volumesPrefix+"/"); ok {
		key, inner, _ := strings.Cut(rest, "/")
		return Part{Volume: key}, inner, key != ""
	}
	return Part{}, "", false
}

// Next returns the next part (io.EOF after the last one). Read it with
// WriteTo before calling Next again.
func (r *Reader) Next() (Part, error) {
	if r.pending == nil {
		if !r.project {
			return Part{}, invalidf("the archive has no project directory")
		}
		for _, v := range r.m.Volumes {
			if !r.seen[v.Key] {
				return Part{}, invalidf("volume %s is missing", v.Key)
			}
		}
		return Part{}, io.EOF
	}
	p, inner, ok := partOf(r.pendName)
	switch {
	case !ok:
		return Part{}, invalidf("unexpected member %s", r.pendName)
	case inner != "" || r.pending.Typeflag != tar.TypeDir:
		return Part{}, invalidf("%s must start with its directory", p.prefix())
	case p.Volume == "" && r.project, p.Volume != "" && r.seen[p.Volume]:
		return Part{}, invalidf("%s appears twice or is not contiguous", p.prefix())
	case p.Volume != "" && !r.project:
		return Part{}, invalidf("the project directory must come before the volumes")
	}
	if p.Volume != "" {
		if _, ok := r.m.Volume(p.Volume); !ok {
			return Part{}, invalidf("volume %s is not in the manifest", p.Volume)
		}
		r.seen[p.Volume] = true
	} else {
		r.project = true
	}
	return p, nil
}

// WriteTo writes the current part to w as a transfer tar (root "./"
// first, names relative to it) and stops at the next part. It validates
// what the agents' extraction relies on before anything reaches them:
// names below the part, hard links to earlier regular files of the same
// part, supported member types.
func (r *Reader) WriteTo(p Part, w io.Writer) (PartStats, error) {
	var st PartStats
	tw := tar.NewWriter(w)
	regular := map[string]bool{}
	for r.pending != nil {
		cur, inner, ok := partOf(r.pendName)
		if !ok || cur != p {
			break
		}
		hdr := r.pending
		out := *hdr
		out.PAXRecords = nil
		out.Format = tar.FormatPAX
		switch {
		case inner == "":
			out.Name = "./"
		case hdr.Typeflag == tar.TypeDir:
			out.Name = inner + "/"
		default:
			out.Name = inner
		}
		switch hdr.Typeflag {
		case tar.TypeDir, tar.TypeSymlink, tar.TypeFifo:
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // TypeRegA from older tools
			out.Typeflag = tar.TypeReg
			regular[inner] = true
		case tar.TypeLink:
			target, tinner, ok := "", "", false
			if t, valid := cleanName(hdr.Linkname); valid {
				var tp Part
				tp, tinner, ok = partOf(t)
				ok = ok && tp == p && regular[tinner]
				target = t
			}
			if !ok {
				return st, invalidf("hard link %s must point to an earlier file of %s (%q)", r.pendName, p.prefix(), target)
			}
			out.Linkname = tinner
		default:
			return st, invalidf("%s has an unsupported type %q", r.pendName, hdr.Typeflag)
		}
		if inner == "" && hdr.Typeflag != tar.TypeDir {
			return st, invalidf("%s must be a directory", p.prefix())
		}
		if err := tw.WriteHeader(&out); err != nil {
			return st, err
		}
		st.Entries++
		if out.Typeflag == tar.TypeReg {
			if r.total += hdr.Size; r.limit > 0 && r.total > r.limit {
				return st, invalidf("the archive unpacks to more than %d bytes", r.limit)
			}
			var src io.Reader = r.tr
			var buf *bytes.Buffer
			if r.wantsCompose(p, inner, hdr.Size) {
				buf = &bytes.Buffer{}
				src = io.TeeReader(r.tr, buf)
			}
			n, err := io.Copy(tw, src)
			if err != nil {
				return st, invalidOrWrite(err)
			}
			st.Bytes += n
			if buf != nil {
				r.compose[inner] = buf.Bytes()
			}
		}
		if err := r.advance(); err != nil {
			return st, err
		}
	}
	return st, tw.Close()
}

// invalidOrWrite marks errors reading the archive as invalid archives and
// passes errors of the destination through.
func invalidOrWrite(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, tar.ErrHeader) || errors.Is(err, gzip.ErrChecksum) ||
		errors.Is(err, gzip.ErrHeader) {
		return invalid(err)
	}
	return err
}

// PinnedName returns the project name a Compose file pins with a
// top-level name: ("" when none does, or only through interpolation).
func PinnedName(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		var doc yaml.Node
		if err := yaml.Unmarshal(files[n], &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			continue
		}
		top := doc.Content[0]
		for i := 0; i+1 < len(top.Content); i += 2 {
			if v := top.Content[i+1]; top.Content[i].Value == "name" && v.Kind == yaml.ScalarNode && !strings.Contains(v.Value, "$") {
				return v.Value
			}
		}
	}
	return ""
}

// ExplicitVolumeNames returns the volumes the Compose files name
// themselves (top-level volumes.<key>.name:, without interpolation), by
// key: they keep their name under any project name.
func ExplicitVolumeNames(files map[string][]byte) map[string]string {
	out := map[string]string{}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		var doc yaml.Node
		if err := yaml.Unmarshal(files[n], &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			continue
		}
		top := doc.Content[0]
		for i := 0; i+1 < len(top.Content); i += 2 {
			if top.Content[i].Value != "volumes" || top.Content[i+1].Kind != yaml.MappingNode {
				continue
			}
			vols := top.Content[i+1]
			for j := 0; j+1 < len(vols.Content); j += 2 {
				key, def := vols.Content[j].Value, vols.Content[j+1]
				if def.Kind != yaml.MappingNode {
					continue
				}
				for k := 0; k+1 < len(def.Content); k += 2 {
					if v := def.Content[k+1]; def.Content[k].Value == "name" && v.Kind == yaml.ScalarNode && v.Value != "" &&
						!strings.Contains(v.Value, "$") {
						out[key] = v.Value
					}
				}
			}
		}
	}
	return out
}

// cleanName validates a member name like the agents' extraction does
// (relative, slash-separated, no "." or ".." segments, no backslashes or
// control characters) and returns it without "./" and trailing "/"
// ("." for the root).
func cleanName(name string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." {
		return ".", true
	}
	if len(name) > 4096 || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return "", false
			}
		}
	}
	return name, true
}
