package main

import (
	"strconv"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A cell's value is what its text was made from: an age in seconds where the
// text rounds it to a day, a quantity in bytes, a share in percent from the
// fraction the text truncates, a pod's state without the init stage kubectl
// puts in front of it. What was not measured has no value, rather than
// reading as zero.
func TestTheCellsCarryWhatTheirTextWasMadeFrom(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cell        view.Cell
		kind        view.ColumnKind
		text, value string
	}{
		{"a quantity", quantityCell("10Gi"), view.KindBytes, "10.0 GiB", "10737418240"},
		{"no quantity", quantityCell(""), view.KindBytes, "", ""},
		{"a share", shareCell("87%", 0.875), view.KindUsage, "87%", "87.5"},
		{"no share", shareCell("", 0), view.KindUsage, "", ""},
		{"pressure", pctCell(12.5, true), view.KindPercent, "12.5%", "12.5"},
		{"no pressure", pctCell(0, false), view.KindPercent, "", ""},
		{"an init crash", podStatusCell("Init:CrashLoopBackOff"), view.KindStatus, "Init:CrashLoopBackOff", "crashloopbackoff"},
		{"an init wait", podStatusCell("Init:0/1"), view.KindStatus, "Init:0/1", "init"},
		{"a running pod", podStatusCell("Running"), view.KindStatus, "Running", ""},
		{"no age", ageCell(time.Time{}), view.KindDuration, "", ""},
	} {
		if tc.cell.Text != tc.text || tc.cell.Value != tc.value {
			t.Errorf("%s: %q valued %q, want %q valued %q", tc.name, tc.cell.Text, tc.cell.Value, tc.text, tc.value)
		}
		if tc.value != "" && !view.ValueOK(tc.kind, tc.cell.Value) {
			t.Errorf("%s: %q is not a value a %s column takes", tc.name, tc.cell.Value, tc.kind)
		}
	}

	aged := ageCell(time.Now().Add(-26 * time.Hour))
	seconds, err := strconv.ParseFloat(aged.Value, 64)
	if aged.Text != "26h" || err != nil || seconds < 26*3600 || seconds > 26*3600+60 {
		t.Errorf("an age of 26h reads %q valued %q", aged.Text, aged.Value)
	}

	two := 2
	deployments := deploymentTable([]deploymentItem{
		{Metadata: meta{Name: "web", CreationTimestamp: time.Now().Add(-3 * time.Hour)}, Spec: struct {
			Replicas *int `json:"replicas"`
		}{&two}},
		{Metadata: meta{Name: "job"}},
	}, false)
	if len(deployments.Values) != 2 || deployments.Values[0] == nil || deployments.Values[0][4] == "" || deployments.Values[1] != nil {
		t.Errorf("the deployments are valued %q, want an age for the first and nothing for the one with no creation time", deployments.Values)
	}
}
