package main

import (
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func statusOf(size, inUse, quota int64) *clientv3.StatusResponse {
	return &clientv3.StatusResponse{DbSize: size, DbSizeInUse: inUse, DbSizeQuota: quota}
}

func TestTheStorageRowIsGradedAgainstThePhysicalFileNotTheLiveData(t *testing.T) {
	// 1.8 of 2 GiB on disk, of which only 0.4 GiB is live: what a database
	// waiting for a defragment looks like. The quota is checked against the
	// file, so this is 90% and red, however little of it is data.
	gib := int64(1 << 30)
	tbl := storageTable(statusOf(18*gib/10, 4*gib/10, 2*gib))

	if len(tbl.Rows) != 1 || tbl.Total != 1 {
		t.Fatalf("rows = %v, want exactly one", tbl.Rows)
	}
	if got := tbl.Rows[0][3]; got != "90.0%" {
		t.Errorf("use = %q, want 90.0%% — graded on DbSize, not DbSizeInUse", got)
	}
	if tbl.Columns[3].Kind != view.KindUsage {
		t.Errorf("use column kind = %q, want %q so the renderer grades it", tbl.Columns[3].Kind, view.KindUsage)
	}
	for _, i := range []int{0, 1, 2} {
		if tbl.Columns[i].Kind != view.KindBytes {
			t.Errorf("column %q kind = %q, want bytes", tbl.Columns[i].Name, tbl.Columns[i].Kind)
		}
	}
}

func TestAServerThatReportsNoQuotaGetsABlankNotAGuess(t *testing.T) {
	// etcd before 3.6 answers without dbSizeQuota, which decodes as zero.
	tbl := storageTable(statusOf(1<<30, 1<<29, 0))
	if got := tbl.Rows[0]; got[2] != "-" || got[3] != "-" {
		t.Errorf("row = %v, want quota and use both \"-\" rather than a percentage of the 2 GiB default", got)
	}
}

func TestTheShareIsRoundedOnceSoTheCellAndItsBandAgree(t *testing.T) {
	// 8996 of 10000 is 89.96%, which prints as "90.0%". The renderer grades
	// the printed text, so the number behind it must already be the rounded
	// one — at or above view.UsageBad, not the raw value just below it.
	share, ok := quotaShare(8996, 10000)
	if !ok {
		t.Fatal("a real quota was reported as absent")
	}
	if share < view.UsageBad {
		t.Errorf("share = %v, want it rounded to %v so it lands in the band \"90.0%%\" is painted in", share, view.UsageBad)
	}
	if got := quotaCell(8996, 10000); got != "90.0%" {
		t.Errorf("cell = %q, want 90.0%%", got)
	}
}

func TestNothingIsComputedFromAValueTheServerCannotHaveSent(t *testing.T) {
	for _, tc := range []struct{ size, quota int64 }{{-1, 1 << 30}, {1 << 30, -1}, {0, 0}} {
		if _, ok := quotaShare(tc.size, tc.quota); ok {
			t.Errorf("quotaShare(%d, %d) reported a share", tc.size, tc.quota)
		}
	}
	if got := quotaCell(0, 1<<30); got != "0.0%" {
		t.Errorf("an empty database against a real quota = %q, want 0.0%%", got)
	}
}

// etcdctl prints every lease ID as %016x of the signed ID, so one below 1<<60
// keeps its leading zero there. Printed unpadded here, the same lease was a
// different string in each tool, and one copied from this table could not be
// found in etcdctl's output by eye or by grep.
func TestALeaseIDReadsTheWayEtcdctlPrintsIt(t *testing.T) {
	v := kvGetResult("/services/api", []byte("10.0.0.1"), 1, 1, 1, 0x0694d7c8e4b7a5f0)
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("want KeyValue, got %s", view.TypeOf(v))
	}
	for _, p := range kv.Pairs {
		if p.Key == "lease" {
			if p.Value != "0694d7c8e4b7a5f0" {
				t.Errorf("lease = %q, want 0694d7c8e4b7a5f0 as etcdctl prints it", p.Value)
			}
			return
		}
	}
	t.Fatal("no lease pair")
}

// The row that stands in for the leases past the limit counts them, and one
// left out is one lease, not "1 more leases".
func TestOneLeaseLeftOutIsCountedInTheSingular(t *testing.T) {
	if got := leasesLeftOut(plugin.SurfaceCLI, 1)[3]; got != "1 more lease; raise --limit" {
		t.Errorf("marker = %q, want the one lease counted in the singular", got)
	}
	if got := leasesLeftOut(plugin.SurfaceCLI, 3)[3]; got != "3 more leases; raise --limit" {
		t.Errorf("marker = %q, want 3 more leases", got)
	}
}

// A lease's times read the way a person types a window. time.Duration prints
// ten minutes as "10m0s" and an hour and a half as "1h30m0s": accepted by
// ParseDuration and typed by nobody, and the seconds figure is never the answer
// to "how long does it have". The mysql plugin's uptime and the redis one's
// spans read "10m" and "1h30m", and an agent comparing the three saw two
// notations for one thing.
func TestALeaseTimesReadTheWayAPersonTypesAWindow(t *testing.T) {
	for _, tc := range []struct {
		granted, remaining    int64
		wantGranted, wantLeft string
	}{
		{600, 599, "10m", "9m59s"},
		{5400, 3600, "1h30m", "1h"},
		{30, 0, "30s", "expired"},
		{30, -1, "30s", "expired"},
	} {
		granted, remaining := leaseTimes(tc.granted, tc.remaining)
		if granted != tc.wantGranted || remaining != tc.wantLeft {
			t.Errorf("leaseTimes(%d, %d) = %q, %q; want %q, %q", tc.granted, tc.remaining,
				granted, remaining, tc.wantGranted, tc.wantLeft)
		}
	}
}

// And it names the limit the way its reader raises one: an agent has an
// argument, not a flag.
func TestTheLeasesLeftOutNameTheLimitAsTheirSurfaceGivesIt(t *testing.T) {
	if got := leasesLeftOut(plugin.SurfaceMCP, 3)[3]; got != `3 more leases; raise the "limit" argument` {
		t.Errorf("marker = %q, want the limit named as an argument", got)
	}
}
