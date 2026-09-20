package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

func TestQuotaPercentValue(t *testing.T) {
	cases := []struct {
		used, hard string
		want       float64
		ok         bool
	}{
		{"2", "4", 0.5, true},
		{"500m", "1", 0.5, true},
		{"8Gi", "16Gi", 0.5, true},
		{"0", "4", 0, true},
		{"1", "0", 0, false},
		{"nope", "4", 0, false},
		{"1", "nope", 0, false},
	}
	for _, c := range cases {
		got, ok := quotaPercentValue(c.used, c.hard)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("quotaPercentValue(%q, %q) = %v, %v; want %v, %v", c.used, c.hard, got, ok, c.want, c.ok)
		}
	}
}

func TestQuotaPercent(t *testing.T) {
	if got := quotaPercent("2", "4"); got != "50%" {
		t.Errorf("quotaPercent(2, 4) = %q, want 50%%", got)
	}
	if got := quotaPercent("1", "0"); got != "" {
		t.Errorf("quotaPercent(1, 0) = %q, want empty on a zero hard limit", got)
	}
}

func TestQuotaPressure(t *testing.T) {
	quotas := list[resourceQuotaItem]{Items: []resourceQuotaItem{
		{
			Metadata: meta{Namespace: "prod", Name: "compute"},
			Status: struct {
				Hard map[string]string `json:"hard"`
				Used map[string]string `json:"used"`
			}{
				Hard: map[string]string{"cpu": "4", "memory": "8Gi"},
				Used: map[string]string{"cpu": "3.6", "memory": "1Gi"},
			},
		},
		{
			Metadata: meta{Namespace: "staging", Name: "compute"},
			Status: struct {
				Hard map[string]string `json:"hard"`
				Used map[string]string `json:"used"`
			}{
				Hard: map[string]string{"cpu": "4"},
				Used: map[string]string{"cpu": "1"},
			},
		},
	}}

	got := quotaPressure(quotas, 0.8)
	if len(got) != 1 {
		t.Fatalf("quotaPressure = %v, want exactly the prod/compute cpu row", got)
	}
	want := "prod/compute cpu: 90%"
	if got[0] != want {
		t.Errorf("quotaPressure[0] = %q, want %q", got[0], want)
	}
}

// **"Forbidden to list limit ranges" and "this namespace has none" are
// different facts**, and both rendered as the quota table on its own — so a
// reader concluded there are no limit ranges from a page that never managed
// to look. The rows that did answer still answer; the part that did not is
// said beside them.
func TestAForbiddenLimitRangeReadIsNotAnAbsenceOfLimitRanges(t *testing.T) {
	quotas := view.Table{Columns: []view.Column{{Name: "Quota"}}, Rows: [][]string{{"compute"}}, Total: 1}
	denied := view.Errorf("kube.forbidden", "limitranges is forbidden for this credential")

	v := quotaView(quotas, list[limitRangeItem]{}, denied)
	s, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("a forbidden read returned %s, want Sections carrying the reason", view.TypeOf(v))
	}
	if len(s.Warnings) == 0 {
		t.Fatal("a forbidden limit-range read left no trace, so it reads as a namespace with none")
	}
	if !strings.Contains(s.Warnings[0].Message, "forbidden") {
		t.Errorf("warning = %q, want it to say the read was refused", s.Warnings[0].Message)
	}
	// The quota rows are not lost to the caveat.
	if inner, ok := s.Items[0].View.(view.Table); !ok || len(inner.Rows) != 1 {
		t.Errorf("the quota rows did not survive: %+v", s.Items)
	}
}

// And a namespace that genuinely has none is still the bare table, so the
// caveat keeps meaning something.
func TestANamespaceWithNoLimitRangesIsStillJustTheTable(t *testing.T) {
	quotas := view.Table{Columns: []view.Column{{Name: "Quota"}}, Rows: [][]string{{"compute"}}, Total: 1}
	if _, ok := quotaView(quotas, list[limitRangeItem]{}, nil).(view.Table); !ok {
		t.Error("a namespace with no limit ranges no longer renders as the quota table alone")
	}
}
