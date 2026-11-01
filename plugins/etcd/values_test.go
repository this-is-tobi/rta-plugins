package main

import (
	"slices"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The sizes, the share of the quota and a lease's times carry their values
// beside their text, which is what a program reading the answer as JSON
// compares instead of a string to take apart; what a member or a server did
// not report has none, rather than reading as zero.
func TestTheBackendsSizesAndALeasesTimesCarryTheirValues(t *testing.T) {
	gib := int64(1 << 30)
	full := storageTable(statusOf(18*gib/10, 4*gib/10, 2*gib))
	if want := []string{"1932735283", "429496729", "2147483648", "90"}; !slices.Equal(full.Values[0], want) {
		t.Errorf("the storage row is valued %q, want %q", full.Values[0], want)
	}
	unknown := storageTable(statusOf(gib, gib/2, 0))
	if want := []string{"1073741824", "536870912", "", ""}; !slices.Equal(unknown.Values[0], want) {
		t.Errorf("a storage row with no quota reported is valued %q, want %q", unknown.Values[0], want)
	}

	one := []memberRow{{id: idLeader, name: "es1", clientURLs: []string{"http://127.0.0.1:2379"}, st: statusOfJSON(t, statusLeader)}}
	one[0].st.DbSize = 20480
	members := membersTable(append(one, memberRow{id: idFollower, name: "es2"}), leaderView{id: idLeader, st: one[0].st})
	db := slices.IndexFunc(members.Columns, func(c view.Column) bool { return c.Name == "DB" })
	if got := members.Values[0][db]; got != "20480" {
		t.Errorf("a member's database is valued %q, want its bytes", got)
	}
	if members.Values[1] != nil {
		t.Errorf("a member that did not answer is valued %q, want nothing", members.Values[1])
	}

	for _, tc := range []struct {
		granted, remaining int64
		want               []string
	}{
		{600, 90, []string{"", "600", "90", ""}},
		{600, -1, []string{"", "600", "0", ""}},
	} {
		var lease view.Table
		lease.Add(leaseRow(7, tc.granted, tc.remaining, 2)...)
		if !slices.Equal(lease.Values[0], tc.want) {
			t.Errorf("a lease granted %ds with %ds left is valued %q, want %q", tc.granted, tc.remaining, lease.Values[0], tc.want)
		}
	}

	for name, tbl := range map[string]view.Table{"storage": full, "members": members} {
		for r, values := range tbl.Values {
			for c, v := range values {
				if v != "" && !view.ValueOK(tbl.Columns[c].Kind, v) {
					t.Errorf("%s row %d %s is valued %q, which a %q column does not take", name, r, tbl.Columns[c].Name, v, tbl.Columns[c].Kind)
				}
			}
		}
	}
}
