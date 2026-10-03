package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// Valkey answers INFO as a redis 7.2.4, the version it keeps for the clients
// that gate on one, and says what it is in valkey_version and its mode in
// server_mode, where redis says redis_mode. Read as redis it was a "7.2.4"
// with no mode, about a server that is neither. The three lines are the ones
// Valkey 8.1.10 answered a cluster node.
func TestValkeyIsNamedForWhatItIsAndShowsItsMode(t *testing.T) {
	valkey := strings.Replace(sampleInfo, "redis_version:7.2.4\r\nredis_mode:standalone",
		"redis_version:7.2.4\r\nserver_name:valkey\r\nvalkey_version:8.1.10\r\nserver_mode:cluster", 1)
	if valkey == sampleInfo {
		t.Fatal("the fixture was not changed")
	}
	srv := newFakeServer(t, map[string]string{"INFO all": bulk(valkey)})
	v, err := run(t, "redis.overview", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := sectionOf(t, v.(view.Sections), "server").(view.KeyValue)
	if got := pairValue(server, "version"); got != "valkey 8.1.10 (answers as redis 7.2.4)" {
		t.Errorf("version = %q", got)
	}
	if got := pairValue(server, "mode"); got != "cluster" {
		t.Errorf("mode = %q", got)
	}

	srv = newFakeServer(t, map[string]string{"INFO all": bulk(sampleInfo)})
	v, err = run(t, "redis.overview", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	server = sectionOf(t, v.(view.Sections), "server").(view.KeyValue)
	if pairValue(server, "version") != "7.2.4" || pairValue(server, "mode") != "standalone" {
		t.Errorf("redis = %v, want it as before", server.Pairs)
	}
}
