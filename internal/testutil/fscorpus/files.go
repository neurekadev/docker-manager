package fscorpus

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// CorpusDir is the committed corpus, relative to the repository root.
const CorpusDir = "test/corpora/fs"

// IndexEntry describes one committed corpus file in index.json.
type IndexEntry struct {
	File             string `json:"file"`
	Format           string `json:"format"`
	Hazard           string `json:"hazard"`
	SHA256           string `json:"sha256"`
	Size             int    `json:"size"`
	UncompressedSize int64  `json:"uncompressedSize,omitempty"`
}

// Files returns every committed corpus file by name.
func Files() (map[string][]byte, error) {
	files := map[string][]byte{}
	// JSON cannot carry invalid UTF-8 (it becomes U+FFFD), so every case
	// also has its exact bytes in pathBase64.
	type jsonCase struct {
		PathCase
		PathBase64 string `json:"pathBase64"`
	}
	cases := TraversalCases()
	out := make([]jsonCase, len(cases))
	for i, c := range cases {
		out[i] = jsonCase{PathCase: c, PathBase64: base64.StdEncoding.EncodeToString([]byte(c.Path))}
	}
	trav, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	files["traversal.json"] = append(trav, '\n')
	index := []IndexEntry{{
		File: "traversal.json", Format: "json",
		Hazard: "hostile path strings; each must be rejected or resolve inside the root",
		SHA256: sum(files["traversal.json"]), Size: len(files["traversal.json"]),
	}}
	for _, a := range Archives() {
		files[a.Name] = a.Data
		index = append(index, IndexEntry{File: a.Name, Format: a.Format, Hazard: a.Hazard, SHA256: sum(a.Data), Size: len(a.Data), UncompressedSize: a.UncompressedSize})
	}
	idx, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, err
	}
	files["index.json"] = append(idx, '\n')
	return files, nil
}

// WriteCorpus writes Files() into dir.
func WriteCorpus(dir string) error {
	files, err := Files()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
