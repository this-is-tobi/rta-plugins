package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A call a message hands its reader reaches the server the message is about.
// Bare, it ran against whatever address the configuration where it was pasted
// names, and the cluster listing a MOVED redirect sends the reader to read
// was a listing of another server's nodes.
func TestACallAMessageHandsOverReachesTheServerItCameFrom(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"TYPE k":       "-MOVED 3999 10.0.0.2:6379\r\n",
		"CLUSTER INFO": "-ERR This instance has cluster support disabled\r\n",
	})
	through := func(capID string, values map[string]any) plugin.Request {
		values["address"] = srv.addr()
		return req(t, capID, values).WithProfile("prod", plugin.TunnelKube)
	}
	run := func(r plugin.Request, capID string) (view.View, error) {
		for _, c := range Plugin().Capabilities {
			if c.ID == capID {
				return c.Run(context.Background(), r)
			}
		}
		t.Fatalf("no capability %q", capID)
		return nil, nil
	}

	_, err := run(through("redis.key.get", map[string]any{"key": "k"}), "redis.key.get")
	ve := view.AsError(err, "x")
	if ve.Code != "redis.cluster.redirect" || !strings.Contains(ve.Hint, "`rta redis cluster --profile prod` lists them") {
		t.Errorf("redirect = %s: %q, want the listing called for the profile", ve.Code, ve.Hint)
	}

	v, err := run(through("redis.cluster", map[string]any{}), "redis.cluster")
	if err != nil {
		t.Fatal(err)
	}
	if body := v.(view.Text).Body; !strings.Contains(body, "`rta redis overview --profile prod` shows its replication") {
		t.Errorf("standalone = %q, want the overview called for the profile", body)
	}
}
