//go:build liveetcd

// The member fan-out against a real three-member cluster, from a machine on the
// cluster's own network: each member advertises a name that only that network
// resolves, so run this inside a container beside them.
//
//	go test -c -tags liveetcd -o etcd.live.test ./plugins/etcd/
//	docker run --rm --network net -v $PWD:/p -e RTA_ETCD_FANOUT=https://m1:2379 \
//	  -e RTA_ETCD_FANOUT_CA=/p/ca.pem alpine /p/etcd.live.test -test.run Fanout -test.v
//
// RTA_ETCD_FANOUT_USER and RTA_ETCD_FANOUT_PASSWORD name an account when the
// cluster has auth on.
package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func fanoutOverview(t *testing.T, extra map[string]any) []view.Pair {
	t.Helper()
	endpoint := os.Getenv("RTA_ETCD_FANOUT")
	if endpoint == "" {
		t.Skip("set RTA_ETCD_FANOUT to a member's client URL — see this file's package comment")
	}
	values := map[string]any{"endpoint": endpoint}
	if ca := os.Getenv("RTA_ETCD_FANOUT_CA"); ca != "" {
		values["ca-file"] = ca
	}
	if user := os.Getenv("RTA_ETCD_FANOUT_USER"); user != "" {
		values["username"], values["password"] = user, os.Getenv("RTA_ETCD_FANOUT_PASSWORD")
	}
	for k, v := range extra {
		values[k] = v
	}
	var c plugin.Capability
	for _, cc := range Plugin().Capabilities {
		if cc.ID == "etcd.overview" {
			c = cc
		}
	}
	v, err := c.Run(context.Background(), req(t, "etcd.overview", values))
	if err != nil {
		t.Fatalf("etcd.member.list: %v", err)
	}
	var pairs []view.Pair
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("unexpected view %s", view.TypeOf(v))
	}
	for _, it := range page.Items {
		tbl, ok := it.View.(view.Table)
		if !ok {
			continue
		}
		health := -1
		for i, col := range tbl.Columns {
			if col.Name == "Health" {
				health = i
			}
		}
		if health < 0 || tbl.Columns[0].Name != "Member" {
			continue
		}
		for _, r := range tbl.Rows {
			pairs = append(pairs, view.Pair{Key: r[0] + " " + r[1], Value: r[health]})
		}
	}
	return pairs
}

// Every member answers its own status over TLS when the certificate vouches
// for the host each advertises, and a username beside it asks them too.
func TestFanoutAsksEveryMember(t *testing.T) {
	rows := fanoutOverview(t, nil)
	if len(rows) != 3 {
		t.Fatalf("members = %v, want three", rows)
	}
	for _, r := range rows {
		t.Logf("%s  %s", r.Key, r.Value)
		if strings.Contains(r.Value, "not asked") || strings.Contains(r.Value, "unreachable") {
			t.Errorf("a member was not asked: %s: %s", r.Key, r.Value)
		}
	}
}

// A username over plaintext leaves the others unasked, saying how to ask them.
func TestFanoutWithholdsTheOthersFromAUsernameOverPlaintext(t *testing.T) {
	rows := fanoutOverview(t, nil)
	notAsked := 0
	for _, r := range rows {
		t.Logf("%s  %s", r.Key, r.Value)
		if strings.Contains(r.Value, "not asked: not over TLS") && strings.Contains(r.Value, "turns TLS on") {
			notAsked++
		}
	}
	if notAsked != 2 {
		t.Errorf("%d members were withheld with the way to turn TLS on, want 2: %v", notAsked, rows)
	}
}
