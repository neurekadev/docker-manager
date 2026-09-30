package observe

import (
	"errors"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Host temperatures from the kernel's hwmon sensors (#146,
// docs/internal/architecture/metrics.md, "Host telemetry"): every
// <sys>/class/hwmon/hwmon<N> directory has a chip name (name) and zero or
// more temperature inputs temp<M>_input in millidegrees Celsius, with an
// optional temp<M>_label. hwmon is not namespaced: a container sees the
// host's sensors whenever sysfs is mounted (privileged or not).

// DefaultSysRoot is where sysfs is read (DOCKER_AGENT_HOST_SYS).
const DefaultSysRoot = "/sys"

// hwmonDir is the hwmon class directory inside sysfs.
const hwmonDir = "class/hwmon"

// Plausible temperatures: a reading outside (minCelsius, maxCelsius) or
// exactly zero (an unconnected input) is left out of the sample.
const (
	minCelsius = -40
	maxCelsius = 150
)

// maxSensorFile bounds a sysfs attribute read (they are a few bytes).
const maxSensorFile = 4 << 10

// hwmonIndex is the number of an hwmon<N> or temp<N>_input name (-1 when
// the name does not have that shape).
func hwmonIndex(name, prefix, suffix string) int {
	s, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return -1
	}
	if s, ok = strings.CutSuffix(s, suffix); !ok || s == "" {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// readAttr reads a small sysfs attribute, trimmed.
func readAttr(fsys fs.FS, name string) (string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxSensorFile))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// sensorText makes a chip name or label safe to send: printable UTF-8
// without control characters, single spaces, trimmed.
func sensorText(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// truncateName cuts a sensor name to at most n bytes on a rune boundary.
func truncateName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n])
}

// plausible reports whether a reading in millidegrees is a real
// temperature.
func plausible(milli int64) bool {
	return milli != 0 && milli > minCelsius*1000 && milli < maxCelsius*1000
}

// readTemperatures reads every hwmon temperature input under sysfs: one
// sample per input, named "<chip>: <label>" ("coretemp: Package id 0",
// "nvme: Composite"), or without a label "<chip>" for temp1 and
// "<chip>: temp<M>" for the others ("acpitz"). Chips are read in the order
// of their hwmon number, so when two chips share a name (two NVMe drives)
// the second's sensors get " (2)" appended, and so on. Inputs that cannot be
// read or report implausible values are absent (gaps, never zero); at most
// protocol.MaxTemperatureSamples are returned. A sysfs without hwmon (a VM,
// no sysfs mounted) has no sensors and no error.
func readTemperatures(fsys fs.FS) ([]protocol.TemperatureSample, error) {
	entries, err := fs.ReadDir(fsys, hwmonDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type chip struct {
		n   int
		dir string
	}
	var chips []chip
	for _, e := range entries {
		if n := hwmonIndex(e.Name(), "hwmon", ""); n >= 0 {
			chips = append(chips, chip{n, hwmonDir + "/" + e.Name()})
		}
	}
	slices.SortFunc(chips, func(a, b chip) int { return a.n - b.n })
	var out []protocol.TemperatureSample
	seen := map[string]bool{}
	chipNames := map[string]int{}
	for _, c := range chips {
		if len(out) >= protocol.MaxTemperatureSamples {
			break
		}
		// Symlinks into /sys/devices: ReadDir follows them.
		files, err := fs.ReadDir(fsys, c.dir)
		if err != nil {
			continue
		}
		name := ""
		if v, err := readAttr(fsys, c.dir+"/name"); err == nil {
			name = sensorText(v)
		}
		if name == "" {
			name = "hwmon" + strconv.Itoa(c.n)
		}
		chipNames[name]++
		suffix := ""
		if k := chipNames[name]; k > 1 {
			suffix = " (" + strconv.Itoa(k) + ")"
		}
		have := map[string]bool{}
		var inputs []int
		for _, f := range files {
			have[f.Name()] = true
			if n := hwmonIndex(f.Name(), "temp", "_input"); n >= 0 {
				inputs = append(inputs, n)
			}
		}
		slices.Sort(inputs)
		for _, n := range inputs {
			if len(out) >= protocol.MaxTemperatureSamples {
				break
			}
			base := c.dir + "/temp" + strconv.Itoa(n)
			if have["temp"+strconv.Itoa(n)+"_fault"] {
				if v, err := readAttr(fsys, base+"_fault"); err == nil && v == "1" {
					continue
				}
			}
			v, err := readAttr(fsys, base+"_input")
			if err != nil {
				continue // EIO, ENODATA: the sensor has no reading now
			}
			milli, err := strconv.ParseInt(v, 10, 64)
			if err != nil || !plausible(milli) {
				continue
			}
			label := ""
			if have["temp"+strconv.Itoa(n)+"_label"] {
				if l, err := readAttr(fsys, base+"_label"); err == nil {
					label = sensorText(l)
				}
			}
			sensor := name
			switch {
			case label != "":
				sensor += ": " + label
			case n != 1:
				sensor += ": temp" + strconv.Itoa(n)
			}
			// Long names keep their " (2)" so they stay apart.
			sensor = truncateName(sensor, protocol.MaxSensorNameLen-len(suffix)) + suffix
			if seen[sensor] {
				continue
			}
			seen[sensor] = true
			out = append(out, protocol.TemperatureSample{Sensor: sensor, Celsius: round2(float64(milli) / 1000)})
		}
	}
	return out, nil
}
