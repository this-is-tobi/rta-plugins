package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A JWT scoped to one collection is refused /cluster with the 403 Qdrant 1.19
// answers it, and still reads that collection's shards. The page goes on
// without the consensus section, says what access would show it, and keeps
// the replicas column, which the per-collection endpoint answers.
func TestACredentialScopedToCollectionsStillShowsItsReplicas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes := map[string]string{
			"/": fxRoot, "/collections": fxCollections, "/collections/docs3": fxInfo,
			"/collections/docs3/cluster": fxShardsDead,
		}
		if r.URL.Path == "/cluster" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":{"error":"Forbidden: Global access is required"},"time":0.000027625}`))
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	v, err := runOverview(t.Context(), req(t, "qdrant.overview",
		map[string]any{"endpoint": strings.TrimPrefix(srv.URL, "http://")}))
	if err != nil {
		t.Fatalf("a credential that may not read the cluster failed the view: %v", err)
	}
	got := pair(section(t, v, "status").(view.KeyValue), "cluster")
	if !strings.HasPrefix(got, "not shown — this credential's access is limited to collections") ||
		!strings.Contains(got, "global read access") {
		t.Errorf("cluster = %q", got)
	}
	if hasSection(v, "peers") {
		t.Error("a peers table was built from a state nobody could read")
	}
	coll := section(t, v, "collections").(view.Table)
	// Peers are named by id when the peer list could not be read.
	want := "fail — 7 of 9 replicas active: shards 0, 2 on 1212972222451972 are Dead"
	if got := coll.Rows[0][len(coll.Rows[0])-1]; got != want {
		t.Errorf("replicas = %q, want %q", got, want)
	}
}

func TestACollectionOfAClusterShowsWhereEveryReplicaIs(t *testing.T) {
	routes := distributedRoutes()
	routes["/collections/docs3/cluster"] = fxShardsRecovering
	f := newFakeQdrant(t, routes)
	v, err := runCollectionShow(t.Context(), reqAt(t, f, "qdrant.collection.show", map[string]any{"collection": "docs3"}))
	if err != nil {
		t.Fatal(err)
	}

	cfg := section(t, v, "collection").(view.KeyValue)
	if got := pair(cfg, "replication factor"); got != "3" {
		t.Errorf("the configuration it always showed is gone: replication factor = %q", got)
	}
	if got := pair(cfg, "replicas"); !strings.HasPrefix(got, "fail — 7 of 9 replicas active") {
		t.Errorf("replicas = %q", got)
	}

	shards := section(t, v, "shards").(view.Table).Rows
	if len(shards) != 9 {
		t.Fatalf("shard rows = %d, want every replica of the three shards", len(shards))
	}
	// Shard 1 on qd3 is being rebuilt and shard 2 on it has not started.
	var recovering, dead []string
	for _, r := range shards {
		switch r[2] {
		case "pending — recovery":
			recovering = append(recovering, r[0]+" "+r[1])
		case "fail — dead":
			dead = append(dead, r[0]+" "+r[1])
		}
	}
	if len(recovering) != 1 || recovering[0] != "1 dbwp-oth-qd3" || len(dead) != 1 || dead[0] != "2 dbwp-oth-qd3" {
		t.Errorf("recovering %v, dead %v", recovering, dead)
	}
	// Points are this peer's alone: a remote replica's count is not known here.
	for _, r := range shards {
		isLocal := strings.HasSuffix(r[1], "(this peer)")
		if isLocal == (r[3] == "-") {
			t.Errorf("points for %v: only the replica on this peer has a count", r)
		}
	}

	transfers := section(t, v, "transfers").(view.Table).Rows
	if len(transfers) != 1 || transfers[0][1] != "dbwp-oth-qd2" || transfers[0][2] != "dbwp-oth-qd3" ||
		transfers[0][3] != "snapshot" || transfers[0][4] != "true" {
		t.Errorf("transfers = %v", transfers)
	}
}

// A collection on a standalone instance is the page it always was: a table of
// shards of one would say nothing.
func TestACollectionOnOneInstanceIsUnchanged(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections/docs3": fxInfo, "/collections/docs3/cluster": fxShardsSolo,
	})
	v, err := runCollectionShow(t.Context(), reqAt(t, f, "qdrant.collection.show", map[string]any{"collection": "docs3"}))
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("view is %T, want the key-value page it was", v)
	}
	if pair(kv, "replicas") != "" {
		t.Errorf("a replica line on an instance with one copy: %v", kv.Pairs)
	}
}
