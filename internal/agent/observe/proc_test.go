package observe

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

const (
	// 8 GB total, 6 GB available: 2 GB used before the ARC is taken out;
	// buffers + cached + reclaimable − shmem = 100 + 2000 + 400 − 500.
	meminfoFull = "MemTotal: 8000000 kB\nMemFree: 1000000 kB\nMemAvailable: 6000000 kB\nBuffers: 100000 kB\nCached: 2000000 kB\n" +
		"SwapCached: 0 kB\nShmem: 500000 kB\nSReclaimable: 400000 kB\nSwapTotal: 4000000 kB\nSwapFree: 3000000 kB\n"
	arcstatsText = "13 1 0x01 123 33456 1234 5678\nname                            type data\nhits                            4    100\n" +
		"size                            4    524288000\nc                               4    1048576000\n"
	diskstatsA = "   8       0 sda 100 0 2000 0 50 0 1000 0 0 0 0\n   8       1 sda1 100 0 2000 0 50 0 1000 0 0 0 0\n" +
		" 259       0 nvme0n1 10 0 100 0 10 0 300 0 0 0 0\n   7       0 loop0 9 0 99999 0 0 0 0 0 0 0 0\n" +
		" 253       0 dm-0 1 0 88888 0 1 0 88888 0 0 0 0\n"
	// sda +2000 sectors read, +1000 written; nvme0n1 +0 read, +1000 written.
	diskstatsB = "   8       0 sda 200 0 4000 0 60 0 2000 0 0 0 0\n   8       1 sda1 200 0 4000 0 60 0 2000 0 0 0 0\n" +
		" 259       0 nvme0n1 10 0 100 0 20 0 1300 0 0 0 0\n   7       0 loop0 9 0 199999 0 0 0 0 0 0 0 0\n"
)

func TestHostMemorySplitsUsedCacheAndZFSARC(t *testing.T) {
	fsys := fstest.MapFS{"meminfo": {Data: []byte(meminfoFull)}}
	m, err := readHostMemory(fsys)
	if err != nil || m.arcErr != nil {
		t.Fatal(err, m.arcErr)
	}
	// Without ZFS: used is total − available, no ARC, cache without shmem.
	if m.used != 2000000*1024 || m.arc != nil || m.cache != 2000000*1024 || m.total != 8000000*1024 {
		t.Fatalf("without ZFS %+v", m)
	}
	if m.swapTotal != 4000000*1024 || m.swapUsed != 1000000*1024 {
		t.Fatalf("swap %+v", m)
	}
	// With ZFS the ARC is its own part, not used.
	fsys[arcStats] = &fstest.MapFile{Data: []byte(arcstatsText)}
	m, err = readHostMemory(fsys)
	if err != nil || m.arcErr != nil {
		t.Fatal(err, m.arcErr)
	}
	if m.arc == nil || *m.arc != 524288000 || m.used != 2000000*1024-524288000 {
		t.Fatalf("with ZFS %+v", m)
	}
	// An ARC not below used (a reading racing a shrinking ARC) is left out.
	fsys[arcStats] = &fstest.MapFile{Data: []byte("size 4 9999999999999\n")}
	if m, _ = readHostMemory(fsys); m.arc != nil || m.used != 2000000*1024 {
		t.Fatalf("oversized ARC %+v", m)
	}
	// A broken arcstats is reported, memory still read.
	fsys[arcStats] = &fstest.MapFile{Data: []byte("size 4 lots\n")}
	if m, err = readHostMemory(fsys); err != nil || m.arcErr == nil || m.arc != nil || m.used != 2000000*1024 {
		t.Fatalf("broken arcstats %+v %v", m, err)
	}
	// Used + ARC + cache never exceed the total.
	fsys["meminfo"] = &fstest.MapFile{Data: []byte("MemTotal: 100 kB\nMemAvailable: 10 kB\nCached: 80 kB\n")}
	delete(fsys, arcStats)
	if m, _ = readHostMemory(fsys); m.used+m.cache > m.total || m.cache != 10*1024 {
		t.Fatalf("clamped cache %+v", m)
	}
}

func TestReadDiskIOSumsWholeDisks(t *testing.T) {
	// Partitions, loop and device-mapper devices are left out (their
	// traffic is already counted on the disks).
	r, w, err := readDiskIO(fstest.MapFS{"diskstats": {Data: []byte(diskstatsA)}})
	if err != nil || r != (2000+100)*512 || w != (1000+300)*512 {
		t.Fatalf("read %d written %d %v", r, w, err)
	}
	for name, text := range map[string]string{
		"no disks":     "   7       0 loop0 9 0 99999 0 0 0 0 0 0 0 0\n",
		"bad counters": "   8       0 sda 100 0 x 0 50 0 1000 0 0 0 0\n",
	} {
		if _, _, err := readDiskIO(fstest.MapFS{"diskstats": {Data: []byte(text)}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHostSamplesMemoryBreakdownAndDiskThroughput(t *testing.T) {
	fsys := procFS(statA, meminfoFull, "0 0 0", "1", netA)
	fsys["diskstats"] = &fstest.MapFile{Data: []byte(diskstatsA)}
	fsys[arcStats] = &fstest.MapFile{Data: []byte(arcstatsText)}
	s := newTestSampler(t, fsys, nil)
	t0 := testutil.Epoch
	h := s.Tick(context.Background(), t0).Host
	if *h.MemoryUsedBytes != 2000000*1024-524288000 || *h.MemoryZFSARCBytes != 524288000 || *h.MemoryCacheBytes != 2000000*1024 ||
		*h.SwapUsedBytes != 1000000*1024 || *h.SwapTotalBytes != 4000000*1024 {
		t.Fatalf("memory %+v", h)
	}
	// The first sample has no previous counters: no throughput (a gap).
	if h.DiskReadBytesPerSecond != nil || h.DiskWriteBytesPerSecond != nil {
		t.Fatalf("first sample has disk rates: %+v", h)
	}
	fsys["diskstats"] = &fstest.MapFile{Data: []byte(diskstatsB)}
	h = s.Tick(context.Background(), t0.Add(10*time.Second)).Host
	if *h.DiskReadBytesPerSecond != 2000*512/10 || *h.DiskWriteBytesPerSecond != 2000*512/10 {
		t.Fatalf("disk rates %v %v", *h.DiskReadBytesPerSecond, *h.DiskWriteBytesPerSecond)
	}
	// Counters going backwards (reboot) give a gap, not a negative.
	fsys["diskstats"] = &fstest.MapFile{Data: []byte(diskstatsA)}
	if h = s.Tick(context.Background(), t0.Add(20*time.Second)).Host; h.DiskReadBytesPerSecond != nil {
		t.Fatalf("rate after a counter reset: %v", *h.DiskReadBytesPerSecond)
	}
	// Live memory leaves the ARC out of used like the samples.
	if o := liveMetrics(t, s); *o.Host.MemoryUsedBytes != 2000000*1024-524288000 {
		t.Fatalf("live used %d", *o.Host.MemoryUsedBytes)
	}
}
