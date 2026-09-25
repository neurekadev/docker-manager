package observe

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
)

// Host telemetry from procfs (docs/architecture/metrics.md, "Host
// telemetry"). The files read here are not namespaced by a container's PID
// or mount namespace except the network statistics: /proc/stat,
// /proc/meminfo, /proc/loadavg and /proc/uptime describe the whole host
// even inside the agent container. Network counters are read from
// <proc>/1/net/dev, the namespace of PID 1: with pid: host that is the
// host's, otherwise the agent container's own.

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

// cpuTimes are the aggregate jiffies of /proc/stat's "cpu" line.
type cpuTimes struct {
	busy, total uint64
}

// readCPU parses /proc/stat: the aggregate line and the number of cpuN
// lines. busy excludes idle and iowait; guest time is already included in
// user/nice and is not added again.
func readCPU(fsys fs.FS) (cpuTimes, int, error) {
	b, err := readProc(fsys, "stat")
	if err != nil {
		return cpuTimes{}, 0, err
	}
	var t cpuTimes
	found, cpus := false, 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			cpus++
			continue
		}
		if len(fields) < 5 {
			return cpuTimes{}, 0, errors.New("/proc/stat: short cpu line")
		}
		var v [8]uint64
		for i := 1; i < len(fields) && i <= 8; i++ {
			n, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				return cpuTimes{}, 0, fmt.Errorf("/proc/stat: %w", err)
			}
			v[i-1] = n
		}
		// user nice system idle iowait irq softirq steal
		t.total = v[0] + v[1] + v[2] + v[3] + v[4] + v[5] + v[6] + v[7]
		t.busy = t.total - v[3] - v[4]
		found = true
	}
	if !found {
		return cpuTimes{}, 0, errors.New("/proc/stat: no cpu line")
	}
	return t, cpus, nil
}

// memInfo is /proc/meminfo in bytes.
type memInfo struct {
	total, available int64
}

func readMem(fsys fs.FS) (memInfo, error) {
	b, err := readProc(fsys, "meminfo")
	if err != nil {
		return memInfo{}, err
	}
	var m memInfo
	var free, buffers, cached int64
	haveAvail := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || n < 0 {
			continue
		}
		if len(fields) > 1 && fields[1] == "kB" {
			n *= 1024
		}
		switch key {
		case "MemTotal":
			m.total = n
		case "MemAvailable":
			m.available, haveAvail = n, true
		case "MemFree":
			free = n
		case "Buffers":
			buffers = n
		case "Cached":
			cached = n
		}
	}
	if m.total <= 0 {
		return memInfo{}, errors.New("/proc/meminfo: no MemTotal")
	}
	if !haveAvail { // kernels before 3.14
		m.available = free + buffers + cached
	}
	m.available = min(max(m.available, 0), m.total)
	return m, nil
}

func readLoad(fsys fs.FS) ([3]float64, error) {
	b, err := readProc(fsys, "loadavg")
	if err != nil {
		return [3]float64{}, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return [3]float64{}, errors.New("/proc/loadavg: short")
	}
	var out [3]float64
	for i := range out {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil || v < 0 {
			return [3]float64{}, fmt.Errorf("/proc/loadavg: bad value %q", fields[i])
		}
		out[i] = v
	}
	return out, nil
}

func readUptime(fsys fs.FS) (int64, error) {
	b, err := readProc(fsys, "uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, errors.New("/proc/uptime: empty")
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("/proc/uptime: bad value %q", fields[0])
	}
	return int64(v), nil
}

// virtualInterface reports interfaces whose traffic is either local or
// already counted on a physical interface (loopback, container veths,
// bridges, overlay/CNI devices).
func virtualInterface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "vxlan", "tunl", "kube", "weave", "cilium", "lxc"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// readNetDev sums the receive and transmit byte counters of the
// non-virtual interfaces in a /proc/<pid>/net/dev file. bridge reports
// Docker's default bridge (docker0), which exists only in the host's
// network namespace.
func readNetDev(fsys fs.FS, name string) (rx, tx uint64, bridge bool, err error) {
	b, err := readProc(fsys, name)
	if err != nil {
		return 0, 0, false, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	seen := false
	for sc.Scan() {
		iface, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue // header lines
		}
		iface = strings.TrimSpace(iface)
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		seen = true
		bridge = bridge || iface == "docker0"
		if virtualInterface(iface) {
			continue
		}
		r, err1 := strconv.ParseUint(fields[0], 10, 64)
		t, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			return 0, 0, false, fmt.Errorf("%s: bad counters for %s", name, iface)
		}
		rx += r
		tx += t
	}
	if !seen {
		return 0, 0, false, fmt.Errorf("%s: no interfaces", name)
	}
	return rx, tx, bridge, nil
}
