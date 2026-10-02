package health

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func active(name string, slot int) protocol.MDMember {
	return protocol.MDMember{Name: name, Slot: slot, State: protocol.MemberActive}
}

func TestParseMDStat(t *testing.T) {
	for _, c := range []struct {
		name  string
		input string
		want  []protocol.MDArray
	}{
		{"none", "Personalities : \nunused devices: <none>\n", nil},
		{"healthy raid1 with bitmap", `Personalities : [raid1]
md0 : active raid1 sdb1[1] sda1[0]
      976630464 blocks super 1.2 [2/2] [UU]
      bitmap: 0/8 pages [0KB], 65536KB chunk

unused devices: <none>
`, []protocol.MDArray{{Name: "md0", Level: "raid1", State: protocol.RAIDHealthy, Devices: 2, Active: 2, SizeBytes: 976630464 * 1024,
			Metadata: "1.2", Bitmap: true, BitmapChunkBytes: 65536 * 1024,
			Members: []protocol.MDMember{active("sdb1", 1), active("sda1", 0)}}}},
		{"degraded raid1", `Personalities : [raid1]
md0 : active raid1 sda1[0]
      976630464 blocks super 1.2 [2/1] [U_]

unused devices: <none>
`, []protocol.MDArray{{Name: "md0", Level: "raid1", State: protocol.RAIDDegraded, Devices: 2, Active: 1, SizeBytes: 976630464 * 1024,
			Metadata: "1.2",
			Members:  []protocol.MDMember{active("sda1", 0)}}}},
		{"recovery with progress", `Personalities : [raid1]
md0 : active raid1 sdc1[2] sda1[0]
      976630464 blocks super 1.2 [2/1] [U_]
      [===>.................]  recovery = 17.3% (169120768/976630464) finish=78.1min speed=172264K/sec
      bitmap: 5/8 pages [20KB], 65536KB chunk

unused devices: <none>
`, []protocol.MDArray{{Name: "md0", Level: "raid1", State: protocol.RAIDRebuilding, Devices: 2, Active: 1, SizeBytes: 976630464 * 1024,
			Metadata: "1.2", Bitmap: true, BitmapChunkBytes: 65536 * 1024,
			Members: []protocol.MDMember{active("sdc1", 2), active("sda1", 0)}, Action: protocol.MDRecovery,
			Progress: f64(17.3), FinishSeconds: i64(4686), SpeedBytesPerSecond: i64(172264 * 1024)}}},
		{"resync delayed", `Personalities : [raid1]
md1 : active raid1 sdd1[1] sdc1[0]
      488254464 blocks super 1.2 [2/2] [UU]
        resync=DELAYED

unused devices: <none>
`, []protocol.MDArray{{Name: "md1", Level: "raid1", State: protocol.RAIDRebuilding, Devices: 2, Active: 2, SizeBytes: 488254464 * 1024,
			Metadata: "1.2",
			Members:  []protocol.MDMember{active("sdd1", 1), active("sdc1", 0)}, Action: protocol.MDResync, Pending: true}}},
		{"auto-read-only resync pending", `Personalities : [raid1]
md127 : active (auto-read-only) raid1 sdb[1] sda[0]
      1000 blocks super 1.2 [2/2] [UU]
        resync=PENDING
`, []protocol.MDArray{{Name: "md127", Level: "raid1", State: protocol.RAIDRebuilding, ReadOnly: true, Devices: 2, Active: 2,
			SizeBytes: 1000 * 1024,
			Metadata:  "1.2",
			Members:   []protocol.MDMember{active("sdb", 1), active("sda", 0)}, Action: protocol.MDResync, Pending: true}}},
		{"check", `Personalities : [raid1]
md0 : active raid1 sdb1[1] sda1[0]
      976630464 blocks super 1.2 [2/2] [UU]
      [=>...................]  check =  5.0% (48831523/976630464) finish=95.4min speed=162000K/sec
`, []protocol.MDArray{{Name: "md0", Level: "raid1", State: protocol.RAIDChecking, Devices: 2, Active: 2, SizeBytes: 976630464 * 1024,
			Metadata: "1.2",
			Members:  []protocol.MDMember{active("sdb1", 1), active("sda1", 0)}, Action: protocol.MDCheck, Progress: f64(5),
			FinishSeconds: i64(5724), SpeedBytesPerSecond: i64(162000 * 1024)}}},
		{"raid5 with failed and spare members", `Personalities : [raid6] [raid5] [raid4]
md2 : active raid5 sde1[4](S) sdd1[3](F) sdc1[2] sdb1[1] sda1[0](W)
      5860150272 blocks super 1.2 level 5, 512k chunk, algorithm 2 [4/3] [UUU_]
      bitmap: 2/15 pages [8KB], 65536KB chunk
`, []protocol.MDArray{{Name: "md2", Level: "raid5", State: protocol.RAIDDegraded, Devices: 4, Active: 3, SizeBytes: 5860150272 * 1024,
			Metadata: "1.2", ChunkBytes: 512 * 1024, Layout: "algorithm 2", Bitmap: true, BitmapChunkBytes: 65536 * 1024,
			Members: []protocol.MDMember{{Name: "sde1", Slot: 4, State: protocol.MemberSpare}, {Name: "sdd1", Slot: 3, State: protocol.MemberFailed},
				active("sdc1", 2), active("sdb1", 1), {Name: "sda1", Slot: 0, State: protocol.MemberActive, WriteMostly: true}}}}},
		{"raid5 lost two members", `md3 : active raid5 sdc1[2](F) sdb1[1](F) sda1[0]
      100 blocks super 1.2 level 5, 512k chunk, algorithm 2 [3/1] [U__]
`, []protocol.MDArray{{Name: "md3", Level: "raid5", State: protocol.RAIDFailed, Devices: 3, Active: 1, SizeBytes: 100 * 1024,
			Metadata: "1.2", ChunkBytes: 512 * 1024, Layout: "algorithm 2",
			Members: []protocol.MDMember{{Name: "sdc1", Slot: 2, State: protocol.MemberFailed}, {Name: "sdb1", Slot: 1, State: protocol.MemberFailed},
				active("sda1", 0)}}}},
		{"raid10 without a working member", `md6 : active raid10 sdd[3](F) sdc[2](F) sdb[1](F) sda[0](F)
      2000 blocks super 1.2 512K chunks 2 near-copies [4/0] [____]
`, []protocol.MDArray{{Name: "md6", Level: "raid10", State: protocol.RAIDFailed, Devices: 4, SizeBytes: 2000 * 1024,
			Metadata: "1.2", ChunkBytes: 512 * 1024, Layout: "2 near-copies",
			Members: []protocol.MDMember{{Name: "sdd", Slot: 3, State: protocol.MemberFailed}, {Name: "sdc", Slot: 2, State: protocol.MemberFailed},
				{Name: "sdb", Slot: 1, State: protocol.MemberFailed}, {Name: "sda", Slot: 0, State: protocol.MemberFailed}}}}},
		{"raid10 with fewer members than its data needs", `md7 : active raid10 sdb[1] sda[0](F)
      2000 blocks super 1.2 512K chunks 2 near-copies [4/1] [_U__]
`, []protocol.MDArray{{Name: "md7", Level: "raid10", State: protocol.RAIDFailed, Devices: 4, Active: 1, SizeBytes: 2000 * 1024,
			Metadata: "1.2", ChunkBytes: 512 * 1024, Layout: "2 near-copies",
			Members: []protocol.MDMember{active("sdb", 1), {Name: "sda", Slot: 0, State: protocol.MemberFailed}}}}},
		{"raid10 that lost one copy of each block", `md8 : active raid10 sdd[3] sdb[1]
      2000 blocks super 1.2 512K chunks 2 near-copies [4/2] [_U_U]
`, []protocol.MDArray{{Name: "md8", Level: "raid10", State: protocol.RAIDDegraded, Devices: 4, Active: 2, SizeBytes: 2000 * 1024,
			Metadata: "1.2", ChunkBytes: 512 * 1024, Layout: "2 near-copies",
			Members: []protocol.MDMember{active("sdd", 3), active("sdb", 1)}}}},
		{"raid10 with three copies", `md9 : active raid10 sda[0]
      1000 blocks super 1.2 512K chunks 3 near-copies [3/1] [U__]
`, []protocol.MDArray{{Name: "md9", Level: "raid10", State: protocol.RAIDDegraded, Devices: 3, Active: 1, SizeBytes: 1000 * 1024,
			Metadata: "1.2", ChunkBytes: 512 * 1024, Layout: "3 near-copies",
			Members: []protocol.MDMember{active("sda", 0)}}}},
		{"raid10 with near and far copies", `md11 : active raid10 sdc[2]
      2000 blocks super 1.2 512K chunks 2 near-copies 2 far-copies [4/1] [__U_]
`, []protocol.MDArray{{Name: "md11", Level: "raid10", State: protocol.RAIDDegraded, Devices: 4, Active: 1, SizeBytes: 2000 * 1024,
			Metadata: "1.2", ChunkBytes: 512 * 1024, Layout: "2 near-copies 2 far-copies",
			Members: []protocol.MDMember{active("sdc", 2)}}}},
		{"raid10 with offset copies", `md12 : active raid10 sdb[1]
      2000 blocks super 1.2 512K chunks 2 offset-copies [4/1] [_U__]
`, []protocol.MDArray{{Name: "md12", Level: "raid10", State: protocol.RAIDFailed, Devices: 4, Active: 1, SizeBytes: 2000 * 1024,
			Metadata: "1.2", ChunkBytes: 512 * 1024, Layout: "2 offset-copies",
			Members: []protocol.MDMember{active("sdb", 1)}}}},
		{"multipath without a working path", `md10 : active multipath sdb[1](F) sda[0](F)
      1000 blocks [2/0] [__]
`, []protocol.MDArray{{Name: "md10", Level: "multipath", State: protocol.RAIDFailed, Devices: 2, SizeBytes: 1000 * 1024, Metadata: "0.90",
			Members: []protocol.MDMember{{Name: "sdb", Slot: 1, State: protocol.MemberFailed}, {Name: "sda", Slot: 0, State: protocol.MemberFailed}}}}},
		{"raid0 without status", `Personalities : [raid0]
md4 : active raid0 sdg1[1] sdf1[0]
      1953260544 blocks super 1.2 512k chunks
`, []protocol.MDArray{{Name: "md4", Level: "raid0", State: protocol.RAIDHealthy, SizeBytes: 1953260544 * 1024,
			Metadata: "1.2", ChunkBytes: 512 * 1024,
			Members: []protocol.MDMember{active("sdg1", 1), active("sdf1", 0)}}}},
		{"inactive", `md5 : inactive sdi1[0](S)
      976630464 blocks super 1.2
`, []protocol.MDArray{{Name: "md5", State: protocol.RAIDInactive, SizeBytes: 976630464 * 1024,
			Metadata: "1.2",
			Members:  []protocol.MDMember{{Name: "sdi1", Slot: 0, State: protocol.MemberSpare}}}}},
		{"multiple arrays", `Personalities : [raid1] [raid10]
md0 : active raid1 sdb1[1] sda1[0]
      1000 blocks super 1.2 [2/2] [UU]

md1 : active raid10 sdf[3] sde[2] sdd[1] sdc[0]
      2000 blocks super 1.2 512K chunks 2 near-copies [4/4] [UUUU]
      [==========>..........]  reshape = 52.1% (1042/2000) finish=0.1min speed=1000K/sec

unused devices: <none>
`, []protocol.MDArray{
			{Name: "md0", Level: "raid1", State: protocol.RAIDHealthy, Devices: 2, Active: 2, SizeBytes: 1000 * 1024,
				Metadata: "1.2",
				Members:  []protocol.MDMember{active("sdb1", 1), active("sda1", 0)}},
			{Name: "md1", Level: "raid10", State: protocol.RAIDRebuilding, Devices: 4, Active: 4, SizeBytes: 2000 * 1024,
				Metadata: "1.2", ChunkBytes: 512 * 1024, Layout: "2 near-copies",
				Members: []protocol.MDMember{active("sdf", 3), active("sde", 2), active("sdd", 1), active("sdc", 0)}, Action: protocol.MDReshape,
				Progress: f64(52.1), FinishSeconds: i64(6), SpeedBytesPerSecond: i64(1000 * 1024)},
		}},
		{"metadata 0.90 (no super) with a bitmap file", `md13 : active raid1 sdb1[1] sda1[0]
      1000 blocks [2/2] [UU]
      bitmap: 1/1 pages [4KB], 65536KB chunk, file: /bitmaps/md13
`, []protocol.MDArray{{Name: "md13", Level: "raid1", State: protocol.RAIDHealthy, Devices: 2, Active: 2, SizeBytes: 1000 * 1024,
			Metadata: "0.90", Bitmap: true, BitmapChunkBytes: 65536 * 1024, Members: []protocol.MDMember{active("sdb1", 1), active("sda1", 0)}}}},
		{"raid6", `md14 : active raid6 sdd[3] sdc[2] sdb[1] sda[0]
      2000 blocks super external:/md127/0 level 6, 64k chunk, algorithm 18 [4/4] [UUUU]
`, []protocol.MDArray{{Name: "md14", Level: "raid6", State: protocol.RAIDHealthy, Devices: 4, Active: 4, SizeBytes: 2000 * 1024,
			Metadata: "external:/md127/0", ChunkBytes: 64 * 1024, Layout: "algorithm 18",
			Members: []protocol.MDMember{active("sdd", 3), active("sdc", 2), active("sdb", 1), active("sda", 0)}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ParseMDStat([]byte(c.input))
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got  %+v\nwant %+v", got, c.want)
			}
			if err := (protocol.HostHealthOutput{SampledAt: testEpoch, SMART: protocol.SMARTReport{Status: protocol.SMARTOK},
				RAID: protocol.RAIDReport{ReadAt: testEpoch, MD: got}}).Validate(); err != nil {
				t.Errorf("parsed arrays do not validate: %v", err)
			}
		})
	}
}

func TestParseMDStatBoundsArrays(t *testing.T) {
	var b []byte
	for i := range protocol.MaxHealthArrays + 5 {
		b = append(b, []byte("md"+strconv.Itoa(i)+" : active raid1 sda1[0] sdb1[1]\n      10 blocks [2/2] [UU]\n\n")...)
	}
	if got := ParseMDStat(b); len(got) != protocol.MaxHealthArrays {
		t.Fatalf("%d arrays", len(got))
	}
}
