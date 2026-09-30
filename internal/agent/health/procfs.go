package health

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// maxProcFile bounds a procfs read.
const maxProcFile = 1 << 20

func readProc(fsys fs.FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxProcFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxProcFile {
		return nil, fmt.Errorf("%s: larger than %d bytes", name, maxProcFile)
	}
	return b, nil
}

// ReadRAID reads the md arrays (/proc/mdstat) and the ZFS pools
// (/proc/spl/kstat/zfs/<pool>/state) from procfs. Missing files are not
// errors (no md driver, no ZFS); other read errors are reported in
// Message and leave that part empty.
func ReadRAID(proc fs.FS, now time.Time) protocol.RAIDReport {
	r := protocol.RAIDReport{ReadAt: now.UTC(), MD: []protocol.MDArray{}, ZFS: []protocol.ZFSPool{}}
	var problems []string
	if b, err := readProc(proc, "mdstat"); err == nil {
		r.MD = ParseMDStat(b)
		if r.MD == nil {
			r.MD = []protocol.MDArray{}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		problems = append(problems, "the software RAID state could not be read")
	}
	pools, err := readZFS(proc)
	if err != nil {
		problems = append(problems, "the ZFS pool state could not be read")
	}
	r.ZFS = pools
	r.Message = strings.Join(problems, "; ")
	return r
}

// zfsDir holds one directory per imported pool with a state file.
const zfsDir = "spl/kstat/zfs"

func readZFS(proc fs.FS) ([]protocol.ZFSPool, error) {
	pools := []protocol.ZFSPool{}
	entries, err := fs.ReadDir(proc, zfsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return pools, nil
	}
	if err != nil {
		return pools, err
	}
	for _, e := range entries {
		if !e.IsDir() || len(pools) >= protocol.MaxHealthArrays {
			continue
		}
		b, err := readProc(proc, path.Join(zfsDir, e.Name(), "state"))
		if err != nil {
			continue // not a pool (or exported meanwhile)
		}
		health := strings.TrimSpace(string(b))
		st, ok := zfsState(health)
		if !ok {
			continue
		}
		pools = append(pools, protocol.ZFSPool{Name: truncate(e.Name(), 255), Health: health, State: st})
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	return pools, nil
}

// zfsState maps a pool's health to its RAID state.
func zfsState(health string) (string, bool) {
	switch health {
	case "ONLINE":
		return protocol.RAIDHealthy, true
	case "DEGRADED":
		return protocol.RAIDDegraded, true
	case "FAULTED", "UNAVAIL", "SUSPENDED", "REMOVED":
		return protocol.RAIDFailed, true
	case "OFFLINE":
		return protocol.RAIDInactive, true
	}
	return "", false
}

// wholeDiskRE matches whole disks (not partitions, loop, device-mapper or
// optical devices) in /proc/partitions.
var wholeDiskRE = regexp.MustCompile(`^(sd[a-z]+|hd[a-z]+|vd[a-z]+|xvd[a-z]+|nvme\d+n\d+)$`)

// WholeDisks lists the real disks the kernel knows (/proc/partitions is
// not namespaced: it lists the host's block devices inside any
// container).
func WholeDisks(proc fs.FS) ([]string, error) {
	b, err := readProc(proc, "partitions")
	if err != nil {
		return nil, err
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 4 {
			continue
		}
		if wholeDiskRE.MatchString(f[3]) {
			out = append(out, f[3])
		}
	}
	return out, nil
}
