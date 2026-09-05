// Command rta-plugin-redis talks to a Redis server: whether it is healthy and
// what it is made of, what shape its keyspace has, who is connected — and,
// behind the write tier, what a key holds, what the server is configured
// with, and what its slow log recorded.
//
// # The line through it
//
// The read tier describes the server: INFO, graded; the keyspace as names and
// counts; the client list; the cluster's view of itself. Three capabilities
// return things somebody stored, and those are writes for what they disclose
// rather than what they change: redis.key.get returns a value; redis.config.get
// returns `requirepass` and `masterauth` alongside everything else; and
// redis.slowlog returns command lines, arguments included — which is to say
// values, on any server where SET was ever slow.
//
// # No dump, on purpose
//
// Redis has no client-side dump. SAVE writes an RDB on the server's own disk,
// and pulling one over the wire means speaking the replication protocol as a
// replica would. The overview says where the last RDB is and how old, which
// is the half of "is this backed up" a client can answer honestly.
//
// # The wire
//
// RESP2 over a plain net.Conn, in this package, rather than a client library.
// Measured before deciding: go-redis adds 4.9 MB to a 2 MB binary, for a
// protocol that is a few hundred lines to speak. Every plugin here is its own
// binary, and a dependency one of them takes is paid by everyone who installs
// it.
//
// Build it and put it on your $PATH as `rta-plugin-redis`:
//
//	cd plugins/redis && go build -o ~/.local/bin/rta-plugin-redis .
//
// State the connection once, in rta's config, under the artifact's own
// section — `rta explain redis.overview` prints the exact heading including
// the digest:
//
//	plugins:
//	  redis@<digest>:
//	    address: cache.internal:6379
//	    tls: true
//
// and export RTA_REDIS_PASSWORD if the server requires one. Every capability
// here reaches off the box, so none of them appear on the automatic dashboard
// on their own; add one explicitly once you have decided polling it is fine:
//
//	dashboard:
//	  tiles:
//	    - id: redis.overview
package main

import (
	"context"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk"
	"github.com/this-is-tobi/rta/pkg/view"
)

func main() { sdk.Serve(Plugin()) }

// withClient is the shape every capability here has: connect, or return the
// classified error; run; close.
func withClient(ctx context.Context, req plugin.Request, fn func(context.Context, *client) (view.View, error)) (view.View, error) {
	c, verr := connect(ctx, req)
	if verr != nil {
		return nil, verr
	}
	defer c.Close()
	return fn(ctx, c)
}

// cap builds a capability with the shared connection inputs appended, so no
// declaration here can forget one and no two can disagree about a default.
//
// Every capability here is NoPreview because every one reaches off the box:
// the automatic dashboard runs Read capabilities unasked, and a cache a
// production service depends on is not something this plugin gets to decide,
// on its own, is fine to poll every few seconds. dashboard.tiles accepts any
// capability regardless, because naming one in a config file is the asking.
func cap(c plugin.Capability, own ...plugin.Field) plugin.Capability {
	c.Inputs = append(own, connFields()...)
	c.NoPreview = true
	return c
}

// version is what this build claims to be, stamped by whatever built it:
// `-X main.version=`, which is the Makefile's flag and GoReleaser's own
// default. A build nobody stamped says "dev" rather than claiming a release
// number that was never cut.
var version = "dev"

func Plugin() plugin.Plugin {
	return plugin.Plugin{
		Name:    "redis",
		Summary: "Redis: health, memory, persistence, replication, the keyspace and the slow log",
		Version: version,
		Capabilities: []plugin.Capability{
			overviewCapability(),
			clientListCapability(),
			clusterCapability(),
			keyListCapability(),
			keyTreeCapability(),
			keyGetCapability(),
			configGetCapability(),
			slowlogCapability(),
			memoryCapability(),
		},
	}
}
