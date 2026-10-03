package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

func clusterServer(t *testing.T, extra map[string]string) *fakeServer {
	t.Helper()
	answers := map[string]string{
		"CLUSTER INFO":  clusterInfoReply,
		"CLUSTER NODES": bulk(clusterNodesText),
	}
	for k, v := range extra {
		answers[k] = v
	}
	return newFakeServer(t, answers)
}

func nodeRows(t *testing.T, v view.View) map[string][]string {
	t.Helper()
	rows := map[string][]string{}
	for _, r := range sectionOf(t, v.(view.Sections), "nodes").(view.Table).Rows {
		rows[r[0]] = r
	}
	return rows
}

// The columns are [ID Address Role Health Replica-of Offset Behind Slots].
func TestAClusterShowsEachNodesOffsetAndHowFarEachReplicaIsBehind(t *testing.T) {
	srv := clusterServer(t, map[string]string{"CLUSTER SHARDS": clusterShardsReply})
	v, err := run(t, "redis.cluster", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := nodeRows(t, v)

	// The replica that stopped: flagged fail by its peers, parked at 211
	// while its primary reads 12837.
	if r := rows["a3de9faa"]; r[3] != "fail" || r[5] != "211" || r[6] != "12.3 KiB" {
		t.Errorf("the stalled replica = %v", r)
	}
	// A replica that kept up, and a primary, which has no primary to be
	// behind.
	if r := rows["4f69015b"]; r[3] != "ok" || r[6] == "-" {
		t.Errorf("a replica that kept up = %v", r)
	}
	if r := rows["1dad7543"]; r[5] != "12837" || r[6] != "-" {
		t.Errorf("a primary = %v", r)
	}
	for _, s := range v.(view.Sections).Items {
		if s.Title == "offsets" {
			t.Errorf("nothing to explain when the offsets were read: %v", s.View)
		}
	}
}

// The flags of the node asked read "myself,master", and the mark was lost to
// the master after it.
func TestTheNodeAskedIsMarkedWhateverItsRole(t *testing.T) {
	srv := clusterServer(t, map[string]string{"CLUSTER SHARDS": clusterShardsReply})
	v, err := run(t, "redis.cluster", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := nodeRows(t, v)["dbf58b41"][2]; got != "primary (this node)" {
		t.Errorf("role = %q", got)
	}
}

// An older server, a proxy that drops the subcommand and an ACL user that may
// not run it each leave the table without the two columns and say why, and
// none of them fails the view.
func TestAClusterWithoutShardsStillShowsItsNodesAndSaysWhy(t *testing.T) {
	for _, tc := range []struct{ name, reply, want string }{
		{"older than 7.0", "-ERR Unknown subcommand or wrong number of arguments for 'SHARDS'. Try CLUSTER HELP.\r\n", "redis added in 7.0"},
		{"an ACL user without it", "-NOPERM User mon has no permissions to run the 'cluster|shards' command\r\n", "+cluster|shards"},
	} {
		srv := clusterServer(t, map[string]string{"CLUSTER SHARDS": tc.reply})
		v, err := run(t, "redis.cluster", srv, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if r := nodeRows(t, v)["a3de9faa"]; r[5] != "-" || r[6] != "-" || r[3] != "fail" {
			t.Errorf("%s: row = %v", tc.name, r)
		}
		note := sectionOf(t, v.(view.Sections), "offsets").(view.Text).Body
		if !strings.Contains(note, tc.want) {
			t.Errorf("%s: note = %q", tc.name, note)
		}
	}
}
