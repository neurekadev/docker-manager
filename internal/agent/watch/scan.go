package watch

import (
	"encoding/binary"
	"errors"
	"hash"
	"hash/fnv"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// Reconciliation scans (#23, #25 Q5): a bounded walk of a scope that never
// follows symlinks and summarizes every directory as a hash of its
// children's names, types, sizes, modification times and permission bits.
// Comparing two scans yields the directories whose listing (or a direct
// child's metadata or content, via size and mtime) changed. Only one hash
// per directory is kept, so memory is bounded by the number of
// directories, not files.

// scan is the result of a walk.
type scan struct {
	// dirs maps root-relative, slash-separated directory paths ("." for the
	// root) to the hash of their children.
	dirs map[string]uint64
	// entries counts the entries visited (below the root).
	entries int
	// truncated: the walk stopped at the entry budget; directories beyond
	// it are unknown.
	truncated bool
}

// visitDir is called for every directory (absolute OS path, root-relative
// slash path) before its entries are read, so a watch added there misses
// nothing created afterwards.
type visitDir func(abs, rel string) error

// errStopWalk ends a walk early without an error.
var errStopWalk = errors.New("watch: stop walk")

// walkTree scans root/prefix (prefix "." for the whole root) with at most
// maxEntries entries. Symlinks are recorded but never followed; entries
// that vanish during the walk are skipped. visit (optional) sees every
// directory; returning errStopWalk ends the walk.
func walkTree(root, prefix string, maxEntries int, visit visitDir) (scan, error) {
	res := scan{dirs: map[string]uint64{}}
	start := root
	if prefix != "." {
		start = filepath.Join(root, filepath.FromSlash(prefix))
	}
	fi, err := os.Lstat(start)
	if err != nil {
		return res, err
	}
	if !fi.IsDir() {
		return res, fs.ErrInvalid // a file or a symlink: nothing to walk
	}
	if prefix != "." {
		// No component of the prefix may be a symlink (Lstat follows the
		// intermediate ones): the walk must stay beneath the root.
		real, rerr := filepath.EvalSymlinks(start)
		rroot, rrerr := filepath.EvalSymlinks(root)
		if rerr != nil || rrerr != nil || real != filepath.Join(rroot, filepath.FromSlash(prefix)) {
			return res, fs.ErrInvalid
		}
	}
	hashes := map[string]hash.Hash64{}
	finish := func(rel string) {
		if h, ok := hashes[rel]; ok {
			res.dirs[rel] = h.Sum64()
			delete(hashes, rel)
		}
	}
	// Directories finish when the walk leaves them; WalkDir visits a
	// directory's entries in lexical order right after the directory, so a
	// stack of open directories suffices.
	var open []string
	closeUntil := func(parent string) {
		for len(open) > 0 && open[len(open)-1] != parent {
			finish(open[len(open)-1])
			open = open[:len(open)-1]
		}
	}
	err = filepath.WalkDir(start, func(p string, d fs.DirEntry, werr error) error {
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if werr != nil {
			if p == start {
				return werr
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir // unreadable directory: its hash stays empty
			}
			return nil // vanished entry
		}
		if p != start {
			parent := path.Dir(rel)
			closeUntil(parent)
			res.entries++
			if maxEntries > 0 && res.entries > maxEntries {
				res.truncated = true
				return fs.SkipAll
			}
			info, ierr := d.Info() // lstat semantics: symlinks are not followed
			if ierr != nil {
				return nil
			}
			h := hashes[parent]
			if h == nil {
				h = fnv.New64a()
				hashes[parent] = h
			}
			writeEntry(h, d.Name(), info)
		}
		if d.IsDir() { // never true for a symlink: WalkDir does not follow them
			hashes[rel] = fnv.New64a()
			open = append(open, rel)
			if visit != nil {
				if verr := visit(p, rel); verr != nil {
					if errors.Is(verr, errStopWalk) {
						res.truncated = true
						return fs.SkipAll
					}
					return verr
				}
			}
		}
		return nil
	})
	if !res.truncated {
		closeUntil("")
	}
	// Directories still open after a truncated walk were not read
	// completely: they are unknown, not recorded.
	return res, err
}

// writeEntry adds one child to its directory's hash. Directories
// contribute name and mode only: their own listing has its own hash, so a
// change deep in a tree reports the directory that changed, not every
// ancestor.
func writeEntry(h hash.Hash64, name string, info fs.FileInfo) {
	var b [8 + 8 + 4 + 1]byte
	_, _ = h.Write([]byte(name))
	if info.IsDir() {
		binary.LittleEndian.PutUint32(b[16:], uint32(info.Mode()))
		_, _ = h.Write(b[16:])
		return
	}
	binary.LittleEndian.PutUint64(b[0:], uint64(info.Size()))               //nolint:gosec // G115: hashed, not interpreted
	binary.LittleEndian.PutUint64(b[8:], uint64(info.ModTime().UnixNano())) //nolint:gosec // G115: hashed
	binary.LittleEndian.PutUint32(b[16:], uint32(info.Mode()))
	b[20] = 0 // separator: names cannot contain NUL
	_, _ = h.Write(b[:])
}

// diffScans returns the directories that differ between old and cur, in
// the subtree prefix ("." for all). Directories missing from a truncated
// cur are unknown, not removed.
func diffScans(old map[string]uint64, cur scan, prefix string) []string {
	var changed []string
	for rel, h := range cur.dirs {
		if oh, ok := old[rel]; !ok || oh != h {
			changed = append(changed, rel)
		}
	}
	if !cur.truncated {
		for rel := range old {
			if _, ok := cur.dirs[rel]; !ok && under(rel, prefix) {
				changed = append(changed, rel)
			}
		}
	}
	return changed
}

// under reports whether rel is prefix or below it (slash paths).
func under(rel, prefix string) bool {
	return prefix == "." || rel == prefix || (len(rel) > len(prefix) && rel[:len(prefix)] == prefix && rel[len(prefix)] == '/')
}
