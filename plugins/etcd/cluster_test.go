package main

import (
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/this-is-tobi/rule-them-all/pkg/view"
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
