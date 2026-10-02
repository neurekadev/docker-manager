package health

import (
	"bufio"
	"bytes"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// /proc/mdstat, as the Linux md driver prints it (drivers/md/md.c,
// md_seq_show):
//
//	Personalities : [raid1] [raid6] [raid5] [raid4]
//	md1 : active raid5 sdd1[3](S) sdc1[2](F) sdb1[1] sda1[0]
//	      1953260544 blocks super 1.2 level 5, 512k chunk, algorithm 2 [3/2] [UU_]
//	      [==>..................]  recovery = 12.6% (123456/976630272) finish=90.2min speed=150000K/sec
//	      bitmap: 0/8 pages [0KB], 65536KB chunk
//
//	md3 : active (auto-read-only) raid1 sdh1[1] sdg1[0]
//	      976630464 blocks super 1.2 [2/2] [UU]
//	        resync=PENDING
//
//	md4 : inactive sdi1[0](S)
//	      976630464 blocks super 1.2
//
//	unused devices: <none>
//
// Each array starts with "<name> : <state> ..." at the start of a line;
// its detail lines are indented. Arrays without redundancy (raid0,
// linear) have no [n/m] status.

var (
	mdHeadRE     = regexp.MustCompile(`^(md[\w/-]*)\s*:\s*(.*)$`)
	mdMemberRE   = regexp.MustCompile(`^(\S+?)\[(\d+)\]((?:\([A-Z]\))*)$`)
	mdStatusRE   = regexp.MustCompile(`\[(\d+)/(\d+)\]\s*\[([U_]+)\]`)
	mdBlocksRE   = regexp.MustCompile(`^(\d+)\s+blocks`)
	mdProgressRE = regexp.MustCompile(`\b(recovery|resync|reshape|check|repair)\s*=\s*([\d.]+)%`)
	mdPendingRE  = regexp.MustCompile(`\b(recovery|resync|reshape|check|repair)\s*=\s*(DELAYED|PENDING)`)
	mdFinishRE   = regexp.MustCompile(`finish\s*=\s*([\d.]+)min`)
	mdSpeedRE    = regexp.MustCompile(`speed\s*=\s*(\d+)K/sec`)
	mdCopiesRE   = regexp.MustCompile(`\b(\d+) (?:near|far|offset)-copies\b`)
	mdSuperRE    = regexp.MustCompile(`\bsuper (\S+)`)
	mdChunkRE    = regexp.MustCompile(`\b(\d+)[kK] chunks?\b`)
	mdAlgoRE     = regexp.MustCompile(`\balgorithm \d+\b`)
	mdBitmapRE   = regexp.MustCompile(`^bitmap:.*?\b(\d+)KB chunk\b`)
)

// ParseMDStat parses /proc/mdstat into arrays (at most
// protocol.MaxHealthArrays, each with at most protocol.MaxHealthMembers
// members). Lines it does not understand are skipped.
func ParseMDStat(b []byte) []protocol.MDArray {
	var out []protocol.MDArray
	var cur *protocol.MDArray
	// copies is the current raid10 array's data copies ("2 near-copies",
	// "2 near-copies 2 far-copies").
	copies := 0
	flush := func() {
		if cur != nil {
			cur.State = mdState(*cur, copies)
			if len(out) < protocol.MaxHealthArrays {
				out = append(out, *cur)
			}
			cur = nil
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			flush()
			m := mdHeadRE.FindStringSubmatch(line)
			if m == nil || strings.HasPrefix(line, "Personalities") || strings.HasPrefix(line, "unused devices") {
				continue
			}
			a := parseMDHead(m[1], m[2])
			cur, copies = &a, 0
			continue
		}
		if cur == nil {
			continue
		}
		detail := strings.TrimSpace(line)
		if n := mdCopies(detail); n > 0 {
			copies = n
		}
		parseMDDetail(cur, detail)
	}
	flush()
	return out
}

// mdCopies is a raid10 detail line's number of data copies: the product
// of its "<n> near-copies" and "<n> far-copies" / "<n> offset-copies"
// (md prints both for a combined layout such as n2f2: 4 copies); 0 when
// the line shows none.
func mdCopies(detail string) int {
	copies := 0
	for _, m := range mdCopiesRE.FindAllStringSubmatch(detail, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 || n > protocol.MaxHealthMembers {
			continue
		}
		copies = max(copies, 1) * n
	}
	return copies
}

// parseMDHead parses "active (auto-read-only) raid1 sdb1[1] sda1[0]".
func parseMDHead(name, rest string) protocol.MDArray {
	a := protocol.MDArray{Name: truncate(name, 64), Members: []protocol.MDMember{}}
	fields := strings.Fields(rest)
	i := 0
	inactive := false
	if i < len(fields) {
		switch fields[i] {
		case "active":
			i++
		case "inactive":
			inactive = true
			i++
		}
	}
	for i < len(fields) && strings.HasPrefix(fields[i], "(") {
		if strings.Contains(fields[i], "read-only") {
			a.ReadOnly = true
		}
		i++
	}
	if !inactive && i < len(fields) && !mdMemberRE.MatchString(fields[i]) {
		a.Level = truncate(fields[i], 32)
		i++
	}
	for ; i < len(fields); i++ {
		m := mdMemberRE.FindStringSubmatch(fields[i])
		if m == nil || len(a.Members) >= protocol.MaxHealthMembers {
			continue
		}
		slot, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		mem := protocol.MDMember{Name: truncate(m[1], 64), Slot: slot, State: protocol.MemberActive}
		flags := m[3]
		switch {
		case strings.Contains(flags, "(F)"):
			mem.State = protocol.MemberFailed
		case strings.Contains(flags, "(S)"):
			mem.State = protocol.MemberSpare
		case strings.Contains(flags, "(J)"):
			mem.State = protocol.MemberJournal
		case strings.Contains(flags, "(R)"):
			mem.State = protocol.MemberReplacement
		}
		mem.WriteMostly = strings.Contains(flags, "(W)")
		a.Members = append(a.Members, mem)
	}
	if inactive {
		a.State = protocol.RAIDInactive
	}
	return a
}

// parseMDDetail reads the indented lines: size, superblock version (absent
// for 0.90, which md does not print), chunk size, layout and [n/m]
// [UU_], the bitmap line, the progress line and resync=DELAYED/PENDING.
func parseMDDetail(a *protocol.MDArray, line string) {
	if m := mdBlocksRE.FindStringSubmatch(line); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n >= 0 && n <= math.MaxInt64/1024 {
			a.SizeBytes = n * 1024
		}
	}
	if m := mdSuperRE.FindStringSubmatch(line); m != nil && mdBlocksRE.MatchString(line) {
		a.Metadata = truncate(strings.TrimSuffix(m[1], ","), 64)
	}
	if m := mdChunkRE.FindStringSubmatch(line); m != nil && mdBlocksRE.MatchString(line) {
		a.ChunkBytes = kib(m[1])
	}
	if m := mdAlgoRE.FindString(line); m != "" {
		a.Layout = m
	} else if cs := mdCopiesRE.FindAllString(line, -1); len(cs) > 0 {
		a.Layout = truncate(strings.Join(cs, " "), 64)
	}
	if strings.HasPrefix(line, "bitmap:") {
		a.Bitmap = true
		if m := mdBitmapRE.FindStringSubmatch(line); m != nil {
			a.BitmapChunkBytes = kib(m[1])
		}
	}
	if m := mdStatusRE.FindStringSubmatch(line); m != nil {
		total, err1 := strconv.Atoi(m[1])
		active, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil && total >= 0 && active >= 0 && active <= total && total <= protocol.MaxHealthMembers*4 {
			a.Devices, a.Active = total, active
		}
	}
	if m := mdProgressRE.FindStringSubmatch(line); m != nil {
		a.Action, a.Pending = m[1], false
		if p, err := strconv.ParseFloat(m[2], 64); err == nil && p >= 0 && p <= 100 {
			a.Progress = &p
		}
		if f := mdFinishRE.FindStringSubmatch(line); f != nil {
			if mins, err := strconv.ParseFloat(f[1], 64); err == nil && mins >= 0 && mins < 1e9 {
				s := int64(math.Round(mins * 60))
				a.FinishSeconds = &s
			}
		}
		if s := mdSpeedRE.FindStringSubmatch(line); s != nil {
			if k, err := strconv.ParseInt(s[1], 10, 64); err == nil && k >= 0 && k <= math.MaxInt64/1024 {
				bps := k * 1024
				a.SpeedBytesPerSecond = &bps
			}
		}
		return
	}
	if m := mdPendingRE.FindStringSubmatch(line); m != nil && a.Action == "" {
		a.Action, a.Pending = m[1], true
	}
}

// kib converts a KiB count to bytes (0 when out of range).
func kib(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/1024 {
		return 0
	}
	return n * 1024
}

// redundancy is how many members a level may lose (-1: unknown).
func redundancy(level string) int {
	switch level {
	case "raid1":
		return math.MaxInt32 // any but the last
	case "raid4", "raid5":
		return 1
	case "raid6":
		return 2
	case "raid0", "linear":
		return 0
	}
	return -1
}

// mdState derives an array's state: inactive; failed when it lost more
// members than its level tolerates (no working member at all, whatever
// the level; raid10 with fewer working members than its data needs; a
// failed member of an array without redundancy); rebuilding while a
// recovery, resync or reshape runs or waits; degraded with missing or
// failed members; checking during a check or repair; else healthy.
// copies is a raid10 array's number of data copies (0: not reported, 2
// assumed).
func mdState(a protocol.MDArray, copies int) string {
	if a.State == protocol.RAIDInactive {
		return protocol.RAIDInactive
	}
	failed := 0
	for _, m := range a.Members {
		if m.State == protocol.MemberFailed {
			failed++
		}
	}
	missing := a.Devices - a.Active
	if a.Devices > 0 && a.Active == 0 {
		return protocol.RAIDFailed
	}
	if a.Level == "raid10" && a.Devices > 0 {
		if copies <= 0 {
			copies = 2
		}
		// Every block lives on copies members: fewer than devices/copies
		// working members cannot hold the data. Losing fewer may still
		// have lost both copies of a block (the layout decides), which
		// /proc/mdstat does not show: degraded.
		if a.Active*copies < a.Devices {
			return protocol.RAIDFailed
		}
	}
	if r := redundancy(a.Level); r >= 0 {
		switch {
		case r == 0 && failed > 0:
			return protocol.RAIDFailed
		case r > 0 && a.Level != "raid1" && a.Devices > 0 && missing > r:
			return protocol.RAIDFailed
		}
	}
	switch a.Action {
	case protocol.MDRecovery, protocol.MDResync, protocol.MDReshape:
		if !a.Pending || missing == 0 {
			return protocol.RAIDRebuilding
		}
	}
	if missing > 0 || failed > 0 {
		return protocol.RAIDDegraded
	}
	if (a.Action == protocol.MDCheck || a.Action == protocol.MDRepair) && !a.Pending {
		return protocol.RAIDChecking
	}
	return protocol.RAIDHealthy
}

// truncate cuts s to n bytes of valid UTF-8.
func truncate(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "")
}
